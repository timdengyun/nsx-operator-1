package subnetset

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmware/vsphere-automation-sdk-go/services/nsxt/model"
	v12 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/vmware-tanzu/nsx-operator/pkg/apis/vpc/v1alpha1"
	"github.com/vmware-tanzu/nsx-operator/pkg/config"
	ctlcommon "github.com/vmware-tanzu/nsx-operator/pkg/controllers/common"
	mock_client "github.com/vmware-tanzu/nsx-operator/pkg/mock/controller-runtime/client"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/subnet"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/subnetbinding"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/subnetport"
	"github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/vpc"
	"github.com/vmware-tanzu/nsx-operator/pkg/util"
)

type fakeRecorder struct{}

func (recorder fakeRecorder) Event(object runtime.Object, eventtype, reason, message string) {
}

func (recorder fakeRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
}

func (recorder fakeRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
}

type fakeOrgRootClient struct{}

func (f fakeOrgRootClient) Get(basePathParam *string, filterParam *string, typeFilterParam *string) (model.OrgRoot, error) {
	return model.OrgRoot{}, nil
}

func (f fakeOrgRootClient) Patch(orgRootParam model.OrgRoot, enforceRevisionCheckParam *bool) error {
	return errors.New("patch error")
}

type fakeSubnetStatusClient struct{}

func (f fakeSubnetStatusClient) List(orgIdParam string, projectIdParam string, vpcIdParam string, subnetIdParam string) (model.VpcSubnetStatusListResult, error) {
	dhcpServerAddress := "1.1.1.1"
	ipAddressType := "fakeIpAddressType"
	networkAddress := "2.2.2.2"
	gatewayAddress := "3.3.3.3"
	return model.VpcSubnetStatusListResult{
		Results: []model.VpcSubnetStatus{
			{
				DhcpServerAddress: &gatewayAddress,
				GatewayAddress:    &dhcpServerAddress,
				IpAddressType:     &ipAddressType,
				NetworkAddress:    &networkAddress,
			},
		},
		Status: nil,
	}, nil
}

func createFakeSubnetSetReconciler(objs []client.Object) *SubnetSetReconciler {
	newScheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(newScheme))
	utilruntime.Must(v1alpha1.AddToScheme(newScheme))
	fakeClient := fake.NewClientBuilder().WithScheme(newScheme).WithObjects(objs...).Build()
	vpcService := &vpc.VPCService{
		Service: common.Service{
			Client:    fakeClient,
			NSXClient: &nsx.Client{},
		},
	}
	subnetService := &subnet.SubnetService{
		Service: common.Service{
			Client: fakeClient,
			NSXClient: &nsx.Client{
				OrgRootClient:      &fakeOrgRootClient{},
				SubnetStatusClient: &fakeSubnetStatusClient{},
			},
			NSXConfig: &config.NSXOperatorConfig{
				CoeConfig: &config.CoeConfig{
					Cluster: "clusterName",
				},
				NsxConfig: &config.NsxConfig{
					EnforcementPoint:   "vmc-enforcementpoint",
					UseAVILoadBalancer: false,
				},
			},
		},
		SubnetStore: &subnet.SubnetStore{},
	}

	subnetPortService := &subnetport.SubnetPortService{
		Service: common.Service{
			Client:    nil,
			NSXClient: &nsx.Client{},
		},
		SubnetPortStore: &subnetport.SubnetPortStore{},
	}

	return &SubnetSetReconciler{
		Client:            fakeClient,
		Scheme:            fake.NewClientBuilder().Build().Scheme(),
		VPCService:        vpcService,
		SubnetService:     subnetService,
		SubnetPortService: subnetPortService,
		Recorder:          &fakeRecorder{},
		StatusUpdater:     ctlcommon.NewStatusUpdater(fakeClient, subnetService.NSXConfig, &fakeRecorder{}, MetricResTypeSubnetSet, "Subnet", "SubnetSet"),
	}
}

func TestReconcile(t *testing.T) {
	subnetsetName := "test-subnetset"
	ns := "test-namespace"
	subnetSet := &v1alpha1.SubnetSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      subnetsetName,
			Namespace: ns,
		},
		Spec: v1alpha1.SubnetSetSpec{},
	}

	testCases := []struct {
		name         string
		expectRes    ctrl.Result
		expectErrStr string
		patches      func(r *SubnetSetReconciler) *gomonkey.Patches
		restoreMode  bool
	}{
		{
			name:      "Create a SubnetSet with find VPCNetworkConfig error",
			expectRes: ResultRequeue,
			patches: func(r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []v1alpha1.SubnetConnectionBindingMap {
					return []v1alpha1.SubnetConnectionBindingMap{}
				})
				return patches
			},
		},
		{
			name:      "Create a SubnetSet",
			expectRes: ResultNormal,
			patches: func(r *SubnetSetReconciler) *gomonkey.Patches {
				vpcnetworkConfig := &v1alpha1.VPCNetworkConfiguration{Spec: v1alpha1.VPCNetworkConfigurationSpec{DefaultSubnetSize: 32}}
				patches := gomonkey.ApplyMethod(reflect.TypeOf(r.VPCService), "GetVPCNetworkConfigByNamespace", func(_ *vpc.VPCService, ns string) (*v1alpha1.VPCNetworkConfiguration, error) {
					return vpcnetworkConfig, nil
				})

				patches.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, key string, value string) []*model.VpcSubnet {
					id1 := "fake-id"
					path := "fake-path"
					vpcSubnet := model.VpcSubnet{Id: &id1, Path: &path}
					return []*model.VpcSubnet{
						&vpcSubnet,
					}
				})
				patches.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []v1alpha1.SubnetConnectionBindingMap {
					return []v1alpha1.SubnetConnectionBindingMap{}
				})
				return patches
			},
		},
		{
			// return nil and requeue when UpdateSubnetSet failed
			name:         "Create a SubnetSet failed to UpdateSubnetSet",
			expectRes:    ResultRequeue,
			expectErrStr: "",
			patches: func(r *SubnetSetReconciler) *gomonkey.Patches {
				vpcnetworkConfig := &v1alpha1.VPCNetworkConfiguration{Spec: v1alpha1.VPCNetworkConfigurationSpec{DefaultSubnetSize: 32}}
				patches := gomonkey.ApplyMethod(reflect.TypeOf(r.VPCService), "GetVPCNetworkConfigByNamespace", func(_ *vpc.VPCService, ns string) (*v1alpha1.VPCNetworkConfiguration, error) {
					return vpcnetworkConfig, nil
				})

				patches.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []v1alpha1.SubnetConnectionBindingMap {
					return []v1alpha1.SubnetConnectionBindingMap{}
				})

				tags := []model.Tag{{Scope: common.String(common.TagScopeVMNamespace), Tag: common.String(ns)}}
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, key string, value string) []*model.VpcSubnet {
					id1 := "fake-id"
					path := "fake-path"
					vpcSubnet := model.VpcSubnet{Id: &id1, Path: &path, Tags: tags}
					return []*model.VpcSubnet{
						&vpcSubnet,
					}
				})

				patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "GenerateSubnetNSTags", func(_ *subnet.SubnetService, obj client.Object) []model.Tag {
					return tags
				})
				return patches
			},
		},
		{
			name:         "Create a SubnetSet with exceed tags",
			expectRes:    ResultNormal,
			expectErrStr: "",
			patches: func(r *SubnetSetReconciler) *gomonkey.Patches {
				vpcnetworkConfig := &v1alpha1.VPCNetworkConfiguration{Spec: v1alpha1.VPCNetworkConfigurationSpec{DefaultSubnetSize: 32}}
				patches := gomonkey.ApplyMethod(reflect.TypeOf(r.VPCService), "GetVPCNetworkConfigByNamespace", func(_ *vpc.VPCService, ns string) (*v1alpha1.VPCNetworkConfiguration, error) {
					return vpcnetworkConfig, nil
				})
				patches.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []*v1alpha1.SubnetConnectionBindingMap {
					return []*v1alpha1.SubnetConnectionBindingMap{}
				})

				patches.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, key string, value string) []*model.VpcSubnet {
					id1 := "fake-id"
					path := "fake-path"
					vpcSubnet := model.VpcSubnet{Id: &id1, Path: &path}
					return []*model.VpcSubnet{
						&vpcSubnet,
					}
				})

				tags := []model.Tag{{Scope: common.String(common.TagScopeSubnetCRUID), Tag: common.String("fake-tag")}}
				for i := 0; i < common.MaxTagsCount; i++ {
					key := fmt.Sprintf("fake-tag-key-%d", i)
					value := common.String(fmt.Sprintf("fake-tag-value-%d", i))
					tags = append(tags, model.Tag{Scope: &key, Tag: value})
				}
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "GenerateSubnetNSTags", func(_ *subnet.SubnetService, obj client.Object) []model.Tag {
					return tags
				})
				return patches
			},
		},
		{
			name:         "Create a SubnetSet success",
			expectRes:    ResultNormal,
			expectErrStr: "",
			patches: func(r *SubnetSetReconciler) *gomonkey.Patches {
				vpcnetworkConfig := &v1alpha1.VPCNetworkConfiguration{Spec: v1alpha1.VPCNetworkConfigurationSpec{DefaultSubnetSize: 32}}
				patches := gomonkey.ApplyMethod(reflect.TypeOf(r.VPCService), "GetVPCNetworkConfigByNamespace", func(_ *vpc.VPCService, ns string) (*v1alpha1.VPCNetworkConfiguration, error) {
					return vpcnetworkConfig, nil
				})
				patches.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []v1alpha1.SubnetConnectionBindingMap {
					return []v1alpha1.SubnetConnectionBindingMap{}
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, key string, value string) []*model.VpcSubnet {
					id1 := "fake-id"
					path := "/orgs/default/projects/nsx_operator_e2e_test/vpcs/subnet-e2e_8f36f7fc-90cd-4e65-a816-daf3ecd6a0f9/subnets/fake-path"
					basicTags1 := util.BuildBasicTags("fakeClusterName", subnetSet, "")
					scopeNamespace := common.TagScopeNamespace
					basicTags1 = append(basicTags1, model.Tag{
						Scope: &scopeNamespace,
						Tag:   &ns,
					})
					basicTags2 := util.BuildBasicTags("fakeClusterName", subnetSet, "")
					ns2 := "ns2"
					basicTags2 = append(basicTags2, model.Tag{
						Scope: &scopeNamespace,
						Tag:   &ns2,
					})
					vpcSubnet1 := model.VpcSubnet{Id: &id1, Path: &path}
					vpcSubnet2 := model.VpcSubnet{Id: &id1, Path: &path, Tags: basicTags1}
					vpcSubnet3 := model.VpcSubnet{Id: &id1, Path: &path, Tags: basicTags2}
					return []*model.VpcSubnet{&vpcSubnet1, &vpcSubnet2, &vpcSubnet3}
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "UpdateSubnetSet", func(_ *subnet.SubnetService, ns string, vpcSubnets []*model.VpcSubnet, tags []model.Tag, dhcpMode string) error {
					return nil
				})
				return patches
			},
		},
		{
			name:         "Restore a SubnetSet success",
			expectRes:    ResultNormal,
			expectErrStr: "",
			restoreMode:  true,
			patches: func(r *SubnetSetReconciler) *gomonkey.Patches {
				vpcnetworkConfig := &v1alpha1.VPCNetworkConfiguration{Spec: v1alpha1.VPCNetworkConfigurationSpec{DefaultSubnetSize: 32}}
				patches := gomonkey.ApplyMethod(reflect.TypeOf(r.VPCService), "GetVPCNetworkConfigByNamespace", func(_ *vpc.VPCService, ns string) (*v1alpha1.VPCNetworkConfiguration, error) {
					return vpcnetworkConfig, nil
				})
				patches.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []v1alpha1.SubnetConnectionBindingMap {
					return []v1alpha1.SubnetConnectionBindingMap{}
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, key string, value string) []*model.VpcSubnet {
					id1 := "fake-id"
					path := "/orgs/default/projects/nsx_operator_e2e_test/vpcs/subnet-e2e_8f36f7fc-90cd-4e65-a816-daf3ecd6a0f9/subnets/fake-path"
					basicTags1 := util.BuildBasicTags("fakeClusterName", subnetSet, "")
					scopeNamespace := common.TagScopeNamespace
					basicTags1 = append(basicTags1, model.Tag{
						Scope: &scopeNamespace,
						Tag:   &ns,
					})
					basicTags2 := util.BuildBasicTags("fakeClusterName", subnetSet, "")
					ns2 := "ns2"
					basicTags2 = append(basicTags2, model.Tag{
						Scope: &scopeNamespace,
						Tag:   &ns2,
					})
					vpcSubnet1 := model.VpcSubnet{Id: &id1, Path: &path}
					vpcSubnet2 := model.VpcSubnet{Id: &id1, Path: &path, Tags: basicTags1}
					vpcSubnet3 := model.VpcSubnet{Id: &id1, Path: &path, Tags: basicTags2}
					return []*model.VpcSubnet{&vpcSubnet1, &vpcSubnet2, &vpcSubnet3}
				})
				patches.ApplyMethod(reflect.TypeOf(r.VPCService), "ListVPCInfo", func(_ *vpc.VPCService, ns string) []common.VPCResourceInfo {
					return []common.VPCResourceInfo{{}}
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "RestoreSubnetSet", func(_ *subnet.SubnetService, obj *v1alpha1.SubnetSet, vpcInfo common.VPCResourceInfo, tags []model.Tag) error {
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "UpdateSubnetSet", func(_ *subnet.SubnetService, ns string, vpcSubnets []*model.VpcSubnet, tags []model.Tag, dhcpMode string) error {
					return nil
				})
				return patches
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.TODO()
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: subnetsetName, Namespace: ns}}

			namespace := &v12.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}

			r := createFakeSubnetSetReconciler([]client.Object{subnetSet, namespace})
			if testCase.patches != nil {
				patches := testCase.patches(r)
				defer patches.Reset()
			}
			r.restoreMode = testCase.restoreMode

			res, err := r.Reconcile(ctx, req)

			if testCase.expectErrStr != "" {
				assert.ErrorContains(t, err, testCase.expectErrStr)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, testCase.expectRes, res)
		})
	}
}

func TestReconcileWithSubnetConnectionBindingMaps(t *testing.T) {
	name := "subnetset"
	ns := "ns1"
	testSubnetSet1 := &v1alpha1.SubnetSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: v1alpha1.SubnetSetSpec{
			AccessMode:     v1alpha1.AccessMode(v1alpha1.AccessModePrivate),
			IPv4SubnetSize: 16,
		},
	}
	testSubnetSet2 := &v1alpha1.SubnetSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Finalizers: []string{
				common.SubnetSetFinalizerName,
			},
		},
		Spec: v1alpha1.SubnetSetSpec{
			AccessMode:     v1alpha1.AccessMode(v1alpha1.AccessModePrivate),
			IPv4SubnetSize: 16,
		},
	}
	deleteTime := metav1.Now()
	testSubnetSet3 := &v1alpha1.SubnetSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Finalizers: []string{
				common.SubnetSetFinalizerName,
			},
			DeletionTimestamp: &deleteTime,
		},
	}
	for _, tc := range []struct {
		name              string
		existingSubnetSet *v1alpha1.SubnetSet
		patches           func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches
		expectErrStr      string
		expectRes         ctrl.Result
	}{
		{
			name:              "Successfully add finalizer after a SubnetSet is used by SubnetConnectionBindingMap",
			existingSubnetSet: testSubnetSet1,
			patches: func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []v1alpha1.SubnetConnectionBindingMap {
					return []v1alpha1.SubnetConnectionBindingMap{{ObjectMeta: metav1.ObjectMeta{Name: "binding1", Namespace: ns}}}
				})
				patches.ApplyMethod(reflect.TypeOf(r.Client), "Update", func(_ client.Client, _ context.Context, obj client.Object, opts ...client.UpdateOption) error {
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, _ string, _ string) []*model.VpcSubnet {
					return []*model.VpcSubnet{}
				})
				return patches
			},
			expectRes: ctrl.Result{},
		}, {
			name:              "Failed to add finalizer after a SubnetSet is used by SubnetConnectionBindingMap",
			existingSubnetSet: testSubnetSet1,
			patches: func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []v1alpha1.SubnetConnectionBindingMap {
					return []v1alpha1.SubnetConnectionBindingMap{{ObjectMeta: metav1.ObjectMeta{Name: "binding1", Namespace: ns}}}
				})
				patches.ApplyMethod(reflect.TypeOf(r.Client), "Update", func(_ client.Client, _ context.Context, obj client.Object, opts ...client.UpdateOption) error {
					return fmt.Errorf("failed to update CR")
				})
				patches.ApplyFunc(updateSubnetSetStatusConditions, func(_ client.Client, _ context.Context, _ *v1alpha1.SubnetSet, newConditions []v1alpha1.Condition) {
					require.Equal(t, 1, len(newConditions))
					cond := newConditions[0]
					assert.Equal(t, "Failed to add the finalizer on SubnetSet for the dependency by SubnetConnectionBindingMap binding1", cond.Message)
				})
				return patches
			},
			expectRes:    ctlcommon.ResultRequeue,
			expectErrStr: "failed to update CR",
		}, {
			name:              "Not add duplicated finalizer after a SubnetSet is used by SubnetConnectionBindingMap",
			existingSubnetSet: testSubnetSet2,
			patches: func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []v1alpha1.SubnetConnectionBindingMap {
					return []v1alpha1.SubnetConnectionBindingMap{{ObjectMeta: metav1.ObjectMeta{Name: "binding1", Namespace: ns}}}
				})
				patches.ApplyMethod(reflect.TypeOf(r.Client), "Update", func(_ client.Client, _ context.Context, obj client.Object, opts ...client.UpdateOption) error {
					assert.FailNow(t, "Should not update SubnetSet CR finalizer")
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, _ string, _ string) []*model.VpcSubnet {
					return []*model.VpcSubnet{}
				})
				patches.ApplyFunc(setSubnetSetReadyStatusTrue, func(_ client.Client, _ context.Context, _ client.Object, _ metav1.Time, _ ...interface{}) {
				})
				return patches
			},
			expectRes: ctrl.Result{},
		}, {
			name:              "Successfully remove finalizer after a Subnet is not used by any SubnetConnectionBindingMap",
			existingSubnetSet: testSubnetSet2,
			patches: func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []v1alpha1.SubnetConnectionBindingMap {
					return []v1alpha1.SubnetConnectionBindingMap{}
				})
				patches.ApplyMethod(reflect.TypeOf(r.Client), "Update", func(_ client.Client, _ context.Context, obj client.Object, opts ...client.UpdateOption) error {
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, _ string, _ string) []*model.VpcSubnet {
					return []*model.VpcSubnet{}
				})
				return patches
			},
			expectRes: ctrl.Result{},
		}, {
			name:              "Failed to remove finalizer after a Subnet is not used by any SubnetConnectionBindingMap",
			existingSubnetSet: testSubnetSet2,
			patches: func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []v1alpha1.SubnetConnectionBindingMap {
					return []v1alpha1.SubnetConnectionBindingMap{}
				})
				patches.ApplyMethod(reflect.TypeOf(r.Client), "Update", func(_ client.Client, _ context.Context, _ client.Object, opts ...client.UpdateOption) error {
					return fmt.Errorf("failed to update CR")
				})
				patches.ApplyFunc(updateSubnetSetStatusConditions, func(_ client.Client, _ context.Context, _ *v1alpha1.SubnetSet, newConditions []v1alpha1.Condition) {
					require.Equal(t, 1, len(newConditions))
					cond := newConditions[0]
					assert.Equal(t, "Failed to remove the finalizer on SubnetSet when there is no reference by SubnetConnectionBindingMaps", cond.Message)
				})
				return patches
			},
			expectRes:    ctlcommon.ResultRequeue,
			expectErrStr: "failed to update CR",
		}, {
			name:              "Not update finalizers if a SubnetSet is not used by any SubnetConnectionBindingMap",
			existingSubnetSet: testSubnetSet1,
			patches: func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []v1alpha1.SubnetConnectionBindingMap {
					return []v1alpha1.SubnetConnectionBindingMap{}
				})
				patches.ApplyMethod(reflect.TypeOf(r.Client), "Update", func(_ client.Client, _ context.Context, obj client.Object, opts ...client.UpdateOption) error {
					assert.FailNow(t, "Should not update SubnetSet CR finalizer")
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, _ string, _ string) []*model.VpcSubnet {
					return []*model.VpcSubnet{}
				})
				patches.ApplyFunc(setSubnetSetReadyStatusTrue, func(_ client.Client, _ context.Context, _ client.Object, _ metav1.Time, _ ...interface{}) {
				})
				return patches
			},
			expectRes: ctrl.Result{},
		}, {
			name:              "Delete a SubnetSet is not allowed if it is used by SubnetConnectionBindingMap",
			existingSubnetSet: testSubnetSet3,
			patches: func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []v1alpha1.SubnetConnectionBindingMap {
					return []v1alpha1.SubnetConnectionBindingMap{}
				})
				patches.ApplyPrivateMethod(reflect.TypeOf(r), "getNSXSubnetBindingsBySubnetSet", func(_ *SubnetSetReconciler, _ string) []*v1alpha1.SubnetConnectionBindingMap {
					return []*v1alpha1.SubnetConnectionBindingMap{{ObjectMeta: metav1.ObjectMeta{Name: "binding1", Namespace: ns}}}
				})
				patches.ApplyPrivateMethod(reflect.TypeOf(r), "setSubnetDeletionFailedStatus", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.Subnet, _ metav1.Time, msg string, reason string) {
					assert.Equal(t, "SubnetSet is used by SubnetConnectionBindingMap binding1 and not able to delete", msg)
					assert.Equal(t, "SubnetSetInUse", reason)
				})
				return patches
			},
			expectRes:    ResultRequeue,
			expectErrStr: "failed to delete SubnetSet CR ns1/subnetset",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.TODO()
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
			r := createFakeSubnetSetReconciler([]client.Object{tc.existingSubnetSet})
			if tc.patches != nil {
				patches := tc.patches(t, r)
				defer patches.Reset()
			}

			res, err := r.Reconcile(ctx, req)

			if tc.expectErrStr != "" {
				assert.EqualError(t, err, tc.expectErrStr)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.expectRes, res)
		})
	}
}

// Test Reconcile - SubnetSet Deletion
func TestReconcile_DeleteSubnetSet(t *testing.T) {
	subnetSetName := "test-subnetset"
	testCases := []struct {
		name              string
		existingSubnetSet *v1alpha1.SubnetSet
		expectRes         ctrl.Result
		expectErrStr      string
		patches           func(r *SubnetSetReconciler) *gomonkey.Patches
	}{
		{
			name: "Delete success",
			existingSubnetSet: &v1alpha1.SubnetSet{
				TypeMeta:   metav1.TypeMeta{},
				ObjectMeta: metav1.ObjectMeta{Name: "fake-subnetSet-uid-2"},
				Spec:       v1alpha1.SubnetSetSpec{},
				Status:     v1alpha1.SubnetSetStatus{},
			},
			patches: func(r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, key string, value string) []*model.VpcSubnet {
					id1 := "fake-id"
					path := "fake-path"
					tags := []model.Tag{
						{Scope: common.String(common.TagScopeSubnetSetCRUID), Tag: common.String("fake-subnetSet-uid-2")},
						{Scope: common.String(common.TagScopeSubnetSetCRName), Tag: common.String(subnetSetName)},
					}
					vpcSubnetSkip := model.VpcSubnet{Id: &id1, Path: &path, Tags: tags}

					id2 := "fake-id-1"
					path2 := "/orgs/default/projects/nsx_operator_e2e_test/vpcs/subnet-xxx/subnets/" + id2
					tagStale := []model.Tag{
						{Scope: common.String(common.TagScopeSubnetSetCRUID), Tag: common.String("fake-subnetSet-uid-stale")},
						{Scope: common.String(common.TagScopeSubnetSetCRName), Tag: common.String(subnetSetName)},
					}
					vpcSubnetDelete := model.VpcSubnet{Id: &id2, Path: &path2, Tags: tagStale}
					return []*model.VpcSubnet{
						&vpcSubnetSkip, &vpcSubnetDelete,
					}
				})

				patches.ApplyMethod(reflect.TypeOf(r.BindingService), "DeleteSubnetConnectionBindingMapsByParentSubnet", func(_ *subnetbinding.BindingService, parentSubnet *model.VpcSubnet) error {
					return nil
				})

				patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "GetPortsOfSubnet", func(_ *subnetport.SubnetPortService, _ string) (ports []*model.VpcSubnetPort) {
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "DeleteSubnet", func(_ *subnet.SubnetService, subnet model.VpcSubnet) error {
					return nil
				})
				return patches
			},
			expectRes: ResultNormal,
		},
		{
			name:         "Delete failed with stale SubnetPort and requeue",
			expectErrStr: "hasStaleSubnetPort: true",
			patches: func(r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, key string, value string) []*model.VpcSubnet {
					id1 := "fake-id"
					path := "fake-path"
					tags := []model.Tag{
						{Scope: common.String(common.TagScopeSubnetSetCRUID), Tag: common.String("fake-subnetSet-uid-2")},
						{Scope: common.String(common.TagScopeSubnetSetCRName), Tag: common.String(subnetSetName)},
					}
					vpcSubnetSkip := model.VpcSubnet{Id: &id1, Path: &path, Tags: tags}

					id2 := "fake-id-1"
					path2 := "/orgs/default/projects/nsx_operator_e2e_test/vpcs/subnet-xxx/subnets/fake-path-2"
					tagStale := []model.Tag{
						{Scope: common.String(common.TagScopeSubnetSetCRUID), Tag: common.String("fake-subnetSet-uid-stale")},
						{Scope: common.String(common.TagScopeSubnetSetCRName), Tag: common.String(subnetSetName)},
					}
					vpcSubnetDelete := model.VpcSubnet{Id: &id2, Path: &path2, Tags: tagStale}
					return []*model.VpcSubnet{
						&vpcSubnetSkip, &vpcSubnetDelete,
					}
				})

				patches.ApplyMethod(reflect.TypeOf(r.BindingService), "DeleteSubnetConnectionBindingMapsByParentSubnet", func(_ *subnetbinding.BindingService, parentSubnet *model.VpcSubnet) error {
					return nil
				})

				patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "IsEmptySubnet", func(_ *subnetport.SubnetPortService, _ string) bool {
					return false
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "DeleteSubnet", func(_ *subnet.SubnetService, subnet model.VpcSubnet) error {
					return nil
				})
				return patches
			},
			expectRes: ResultRequeue,
		},
		{
			name:         "Delete NSX Subnet failed and requeue",
			expectErrStr: "multiple errors occurred while deleting Subnets",
			patches: func(r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, key string, value string) []*model.VpcSubnet {
					id1 := "fake-id"
					path := "fake-path"
					tags := []model.Tag{
						{Scope: common.String(common.TagScopeSubnetSetCRUID), Tag: common.String("fake-subnetSet-uid-2")},
						{Scope: common.String(common.TagScopeSubnetSetCRName), Tag: common.String(subnetSetName)},
					}
					vpcSubnetSkip := model.VpcSubnet{Id: &id1, Path: &path, Tags: tags}

					id2 := "fake-id-1"
					path2 := "/orgs/default/projects/nsx_operator_e2e_test/vpcs/subnet-xxx/subnets/fake-path-2"
					tagStale := []model.Tag{
						{Scope: common.String(common.TagScopeSubnetSetCRUID), Tag: common.String("fake-subnetSet-uid-stale")},
						{Scope: common.String(common.TagScopeSubnetSetCRName), Tag: common.String(subnetSetName)},
					}
					vpcSubnetDelete := model.VpcSubnet{Id: &id2, Path: &path2, Tags: tagStale}
					return []*model.VpcSubnet{
						&vpcSubnetSkip, &vpcSubnetDelete,
					}
				})

				patches.ApplyMethod(reflect.TypeOf(r.BindingService), "DeleteSubnetConnectionBindingMapsByParentSubnet", func(_ *subnetbinding.BindingService, parentSubnet *model.VpcSubnet) error {
					return nil
				})

				patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "GetPortsOfSubnet", func(_ *subnetport.SubnetPortService, _ string) (ports []*model.VpcSubnetPort) {
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "DeleteSubnet", func(_ *subnet.SubnetService, subnet model.VpcSubnet) error {
					return errors.New("delete NSX Subnet failed")
				})
				return patches
			},
			expectRes: ResultRequeue,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.TODO()
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: subnetSetName, Namespace: "default"}}
			var objs []client.Object
			if testCase.existingSubnetSet != nil {
				objs = append(objs, testCase.existingSubnetSet)
			}
			r := createFakeSubnetSetReconciler(objs)
			patches := testCase.patches(r)
			defer patches.Reset()

			res, err := r.Reconcile(ctx, req)

			if testCase.expectErrStr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, testCase.expectErrStr)
			}
			assert.Equal(t, testCase.expectRes, res)
		})
	}
}

// Test Reconcile - SubnetSet Deletion
func TestReconcile_DeleteSubnetSet_WithFinalizer(t *testing.T) {
	ctx := context.TODO()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-subnetset", Namespace: "default"}}

	subnetset := &v1alpha1.SubnetSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-subnetset",
			Namespace:         "default",
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
			Finalizers:        []string{"test-Finalizers"},
		},
		Spec: v1alpha1.SubnetSetSpec{},
	}

	r := createFakeSubnetSetReconciler([]client.Object{subnetset})

	patches := gomonkey.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, key string, value string) []*model.VpcSubnet {
		id1 := "fake-id"
		path := "/orgs/default/projects/nsx_operator_e2e_test/vpcs/subnet-e2e_8f36f7fc-90cd-4e65-a816-daf3ecd6a0f9/subnets/" + id1
		vpcSubnet := model.VpcSubnet{Id: &id1, Path: &path}
		return []*model.VpcSubnet{
			&vpcSubnet,
		}
	})

	defer patches.Reset()

	patches.ApplyPrivateMethod(reflect.TypeOf(r), "getSubnetBindingCRsBySubnetSet", func(_ *SubnetSetReconciler, _ context.Context, _ *v1alpha1.SubnetSet) []v1alpha1.SubnetConnectionBindingMap {
		return []v1alpha1.SubnetConnectionBindingMap{}
	})

	patches.ApplyPrivateMethod(reflect.TypeOf(r), "getNSXSubnetBindingsBySubnetSet", func(_ *SubnetSetReconciler, _ string) []*v1alpha1.SubnetConnectionBindingMap {
		return []*v1alpha1.SubnetConnectionBindingMap{}
	})

	patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "GetPortsOfSubnet", func(_ *subnetport.SubnetPortService, _ string) (ports []*model.VpcSubnetPort) {
		return nil
	})

	patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "DeleteSubnet", func(_ *subnet.SubnetService, subnet model.VpcSubnet) error {
		return nil
	})

	res, err := r.Reconcile(ctx, req)

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, res)
}

// Test Merge SubnetSet Status Condition
func TestMergeSubnetSetStatusCondition(t *testing.T) {
	subnetset := &v1alpha1.SubnetSet{
		Status: v1alpha1.SubnetSetStatus{
			Conditions: []v1alpha1.Condition{
				{
					Type:   v1alpha1.Ready,
					Status: v12.ConditionStatus(metav1.ConditionFalse),
				},
			},
		},
	}

	newCondition := v1alpha1.Condition{
		Type:   v1alpha1.Ready,
		Status: v12.ConditionStatus(metav1.ConditionTrue),
	}

	updated := mergeSubnetSetStatusCondition(subnetset, &newCondition)

	assert.True(t, updated)
	assert.Equal(t, v12.ConditionStatus(metav1.ConditionTrue), subnetset.Status.Conditions[0].Status)
}

// Test deleteSubnetBySubnetSetName
func TestDeleteSubnetBySubnetSetName(t *testing.T) {
	ctx := context.TODO()

	r := createFakeSubnetSetReconciler(nil)

	patches := gomonkey.ApplyMethod(reflect.TypeOf(r.SubnetService), "ListSubnetBySubnetSetName", func(_ *subnet.SubnetService, ns, subnetSetName string) []*model.VpcSubnet {
		return []*model.VpcSubnet{}
	})
	defer patches.Reset()

	err := r.deleteSubnetBySubnetSetName(ctx, "test-subnetset", "default")
	assert.NoError(t, err)
}

func TestSubnetSetReconciler_CollectGarbage(t *testing.T) {
	r := createFakeSubnetSetReconciler(nil)

	ctx := context.TODO()

	subnetSet := v1alpha1.SubnetSet{
		ObjectMeta: metav1.ObjectMeta{
			UID:       "fake-subnetset-uid",
			Name:      "test-subnetset",
			Namespace: "test-namespace",
		},
	}
	subnetSetList := &v1alpha1.SubnetSetList{
		TypeMeta: metav1.TypeMeta{},
		ListMeta: metav1.ListMeta{},
		Items:    []v1alpha1.SubnetSet{subnetSet},
	}

	patches := gomonkey.ApplyFunc(listSubnetSet, func(c client.Client, ctx context.Context, options ...client.ListOption) (*v1alpha1.SubnetSetList, error) {
		return subnetSetList, nil
	})
	defer patches.Reset()

	patches.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, key string, value string) []*model.VpcSubnet {
		id1 := "fake-id"
		path := "/orgs/default/projects/nsx_operator_e2e_test/vpcs/subnet-e2e_8f36f7fc-90cd-4e65-a816-daf3ecd6a0f9/subnets/fake-path"
		vpcSubnet1 := model.VpcSubnet{Id: &id1, Path: &path}
		return []*model.VpcSubnet{
			&vpcSubnet1,
		}
	})
	patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "GetPortsOfSubnet", func(_ *subnetport.SubnetPortService, _ string) (ports []*model.VpcSubnetPort) {
		return nil
	})
	patches.ApplyMethod(reflect.TypeOf(r.BindingService), "DeleteSubnetConnectionBindingMapsByParentSubnet", func(_ *subnetbinding.BindingService, parentSubnet *model.VpcSubnet) error {
		return nil
	})
	patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "DeleteSubnet", func(_ *subnet.SubnetService, subnet model.VpcSubnet) error {
		return nil
	})

	patches.ApplyMethod(reflect.TypeOf(&common.ResourceStore{}), "ListIndexFuncValues", func(_ *common.ResourceStore, _ string) sets.Set[string] {
		res := sets.New[string]("fake-subnetSet-uid-2")
		return res
	})
	// ListSubnetCreatedBySubnetSet
	patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "ListSubnetCreatedBySubnetSet", func(_ *subnet.SubnetService, id string) []*model.VpcSubnet {
		id1 := "fake-id"
		path := "/orgs/default/projects/nsx_operator_e2e_test/vpcs/subnet-e2e_8f36f7fc-90cd-4e65-a816-daf3ecd6a0f9/subnets/fake-path"
		vpcSubnet1 := model.VpcSubnet{Id: &id1, Path: &path}
		invalidPath := "fakePath"
		vpcSubnet2 := model.VpcSubnet{Id: &id1, Path: &invalidPath}
		return []*model.VpcSubnet{
			&vpcSubnet1, &vpcSubnet2,
		}
	})

	// fake SubnetSetLocks
	lock := sync.Mutex{}
	subnetSetId := types.UID(uuid.NewString())
	ctlcommon.SubnetSetLocks.LoadOrStore(subnetSetId, &lock)

	r.CollectGarbage(ctx)
	// the lock for should be deleted
	_, ok := ctlcommon.SubnetSetLocks.Load(subnetSetId)
	assert.False(t, ok)
}

func TestSubnetSetReconciler_deleteSubnetForSubnetSet(t *testing.T) {
	r := createFakeSubnetSetReconciler(nil)
	r.EnableRestoreMode()
	subnetSet := v1alpha1.SubnetSet{
		ObjectMeta: metav1.ObjectMeta{
			UID:       "fake-subnetset-uid",
			Name:      "test-subnetset",
			Namespace: "test-namespace",
		},
		Status: v1alpha1.SubnetSetStatus{
			Subnets: []v1alpha1.SubnetInfo{
				{
					NetworkAddresses:    []string{"10.0.0.0/28"},
					GatewayAddresses:    []string{"10.0.0.1/28"},
					DHCPServerAddresses: []string{"10.0.0.3/28"},
				},
			},
		},
	}

	vpcSubnet1 := &model.VpcSubnet{
		Id:          common.String("subnet-1"),
		Path:        common.String("/orgs/default/projects/default/vpcs/vpcs/subnets/subnet-1"),
		IpAddresses: []string{"10.0.0.16/28"},
	}
	vpcSubnet2 := &model.VpcSubnet{
		Id:          common.String("subnet-2"),
		Path:        common.String("/orgs/default/projects/default/vpcs/vpcs/subnets/subnet-2"),
		IpAddresses: []string{"10.0.0.0/28"},
	}

	patches := gomonkey.ApplyMethod(reflect.TypeOf(r.SubnetService.SubnetStore), "GetByIndex", func(_ *subnet.SubnetStore, key string, value string) []*model.VpcSubnet {
		return []*model.VpcSubnet{
			vpcSubnet1,
			vpcSubnet2,
		}
	})
	defer patches.Reset()
	patches.ApplyPrivateMethod(reflect.TypeOf(r), "deleteSubnets", func(_ *SubnetSetReconciler, nsxSubnets []*model.VpcSubnet, deleteBindingMaps bool) (hasStalePort bool, err error) {
		assert.Equal(t, vpcSubnet1, nsxSubnets[0])
		assert.Equal(t, false, deleteBindingMaps)
		return false, nil
	})
	err := r.deleteSubnetForSubnetSet(subnetSet, true, false)
	assert.Nil(t, err)
}

type MockManager struct {
	ctrl.Manager
	client client.Client
	scheme *runtime.Scheme
}

func (m *MockManager) GetClient() client.Client {
	return m.client
}

func (m *MockManager) GetScheme() *runtime.Scheme {
	return m.scheme
}

func (m *MockManager) GetEventRecorderFor(name string) record.EventRecorder {
	return nil
}

func (m *MockManager) Add(runnable manager.Runnable) error {
	return nil
}

func (m *MockManager) Start(context.Context) error {
	return nil
}

type mockWebhookServer struct{}

func (m *mockWebhookServer) Register(path string, hook http.Handler) {
	return
}

func (m *mockWebhookServer) Start(ctx context.Context) error {
	return nil
}

func (m *mockWebhookServer) StartedChecker() healthz.Checker {
	return nil
}

func (m *mockWebhookServer) WebhookMux() *http.ServeMux {
	return nil
}

func (m *mockWebhookServer) NeedLeaderElection() bool {
	return true
}

func TestStartSubnetSetController(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithObjects().Build()
	vpcService := &vpc.VPCService{
		Service: common.Service{
			Client: fakeClient,
		},
	}
	subnetService := &subnet.SubnetService{
		Service: common.Service{
			Client: fakeClient,
		},
		SubnetStore: &subnet.SubnetStore{},
	}
	subnetPortService := &subnetport.SubnetPortService{
		Service:         common.Service{},
		SubnetPortStore: nil,
	}
	subnetBindingService := &subnetbinding.BindingService{
		Service:      common.Service{},
		BindingStore: nil,
	}

	mockMgr := &MockManager{scheme: runtime.NewScheme()}

	testCases := []struct {
		name          string
		expectErrStr  string
		webHookServer webhook.Server
		patches       func() *gomonkey.Patches
	}{
		// expected no error when starting the SubnetSet controller with webhook
		{
			name:          "StartSubnetSetController with webhook",
			webHookServer: &mockWebhookServer{},
			patches: func() *gomonkey.Patches {
				patches := gomonkey.ApplyFunc(ctlcommon.GenericGarbageCollector, func(cancel chan bool, timeout time.Duration, f func(ctx context.Context) error) {
					return
				})
				patches.ApplyMethod(reflect.TypeOf(&ctrl.Builder{}), "Complete", func(_ *ctrl.Builder, r reconcile.Reconciler) error {
					return nil
				})
				patches.ApplyPrivateMethod(reflect.TypeOf(&SubnetSetReconciler{}), "setupWithManager", func(_ *SubnetSetReconciler, mgr ctrl.Manager) error {
					return nil
				})
				return patches
			},
		},
		// expected no error when starting the SubnetSet controller without webhook
		{
			name:          "StartSubnetSetController without webhook",
			webHookServer: nil,
			patches: func() *gomonkey.Patches {
				patches := gomonkey.ApplyFunc(ctlcommon.GenericGarbageCollector, func(cancel chan bool, timeout time.Duration, f func(ctx context.Context) error) {
					return
				})
				patches.ApplyMethod(reflect.TypeOf(&ctrl.Builder{}), "Complete", func(_ *ctrl.Builder, r reconcile.Reconciler) error {
					return nil
				})
				patches.ApplyPrivateMethod(reflect.TypeOf(&SubnetSetReconciler{}), "setupWithManager", func(_ *SubnetSetReconciler, mgr ctrl.Manager) error {
					return nil
				})
				return patches
			},
		},
		{
			name:          "StartSubnetSetController return error",
			expectErrStr:  "failed to setupWithManager",
			webHookServer: &mockWebhookServer{},
			patches: func() *gomonkey.Patches {
				patches := gomonkey.ApplyFunc(ctlcommon.GenericGarbageCollector, func(cancel chan bool, timeout time.Duration, f func(ctx context.Context) error) {
					return
				})
				patches.ApplyMethod(reflect.TypeOf(&ctrl.Builder{}), "Complete", func(_ *ctrl.Builder, r reconcile.Reconciler) error {
					return nil
				})
				patches.ApplyPrivateMethod(reflect.TypeOf(&SubnetSetReconciler{}), "setupWithManager", func(_ *SubnetSetReconciler, mgr ctrl.Manager) error {
					return errors.New("failed to setupWithManager")
				})
				return patches
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			patches := testCase.patches()
			defer patches.Reset()

			reconcile := NewSubnetSetReconciler(mockMgr, subnetService, subnetPortService, vpcService, subnetBindingService)
			err := reconcile.StartController(mockMgr, testCase.webHookServer)

			if testCase.expectErrStr != "" {
				assert.ErrorContains(t, err, testCase.expectErrStr)
			} else {
				assert.NoError(t, err, "expected no error when starting the SubnetSet controller")
			}
		})
	}
}

func TestDeleteSubnets(t *testing.T) {
	nsxSubnets := []*model.VpcSubnet{{
		Id:   common.String("net1"),
		Path: common.String("subnet1-path"),
	}, {
		Id:   common.String("net2"),
		Path: common.String("subnet2-path"),
	}}
	testLock := &sync.Mutex{}
	for _, tc := range []struct {
		name              string
		nsxSubnets        []*model.VpcSubnet
		deleteBindingMaps bool
		patches           func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches
		expHasStalePort   bool
		expErrStr         string
	}{
		{
			name:              "No NSX subnets found",
			nsxSubnets:        []*model.VpcSubnet{},
			deleteBindingMaps: false,
			expHasStalePort:   false,
			expErrStr:         "",
		}, {
			name:              "One of the NSX subnet has stale ports",
			nsxSubnets:        nsxSubnets,
			deleteBindingMaps: false,
			patches: func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyFunc(ctlcommon.LockSubnetSet, func(uuid types.UID) *sync.Mutex {
					testLock.Lock()
					return testLock
				})
				patches.ApplyFunc(ctlcommon.UnlockSubnetSet, func(_ types.UID, subnetSetLock *sync.Mutex) {
					testLock.Unlock()
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "IsEmptySubnet", func(_ *subnetport.SubnetPortService, id string, path string) bool {
					if id == "net1" {
						return false
					}
					return true
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "DeleteSubnet", func(_ *subnet.SubnetService, nsxSubnet model.VpcSubnet) error {
					if *nsxSubnet.Id == "net1" {
						require.Fail(t, "SubnetService.DeleteSubnet should not be called if stale ports exist")
					}
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "DeletePortCount", func(_ *subnetport.SubnetPortService, _ string) {})
				return patches
			},
			expHasStalePort: true,
			expErrStr:       "",
		}, {
			name:              "Failed to delete NSX subnets",
			nsxSubnets:        nsxSubnets,
			deleteBindingMaps: false,
			patches: func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyFunc(ctlcommon.LockSubnetSet, func(uuid types.UID) *sync.Mutex {
					testLock.Lock()
					return testLock
				})
				patches.ApplyFunc(ctlcommon.UnlockSubnetSet, func(uuid types.UID, subnetSetLock *sync.Mutex) {
					testLock.Unlock()
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "IsEmptySubnet", func(_ *subnetport.SubnetPortService, id string, path string) bool {
					return true
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "DeleteSubnet", func(_ *subnet.SubnetService, nsxSubnet model.VpcSubnet) error {
					if *nsxSubnet.Id == "net1" {
						return fmt.Errorf("net1 deletion failed")
					}
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "DeletePortCount", func(_ *subnetport.SubnetPortService, _ string) {})
				return patches
			},
			expHasStalePort: false,
			expErrStr:       "multiple errors occurred while deleting Subnets: [failed to delete NSX Subnet/net1: net1 deletion failed]",
		}, {
			name:              "Succeeded to delete NSX subnets",
			nsxSubnets:        nsxSubnets,
			deleteBindingMaps: false,
			patches: func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyFunc(ctlcommon.LockSubnetSet, func(uuid types.UID) *sync.Mutex {
					testLock.Lock()
					return testLock
				})
				patches.ApplyFunc(ctlcommon.UnlockSubnetSet, func(uuid types.UID, subnetSetLock *sync.Mutex) {
					testLock.Unlock()
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "IsEmptySubnet", func(_ *subnetport.SubnetPortService, id string, path string) bool {
					return true
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "DeleteSubnet", func(_ *subnet.SubnetService, nsxSubnet model.VpcSubnet) error {
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "DeletePortCount", func(_ *subnetport.SubnetPortService, _ string) {})
				return patches
			},
			expHasStalePort: false,
			expErrStr:       "",
		}, {
			name:              "Failed to delete NSX subnet connection binding maps",
			nsxSubnets:        nsxSubnets,
			deleteBindingMaps: true,
			patches: func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyFunc(ctlcommon.LockSubnetSet, func(uuid types.UID) *sync.Mutex {
					testLock.Lock()
					return testLock
				})
				patches.ApplyFunc(ctlcommon.UnlockSubnetSet, func(uuid types.UID, subnetSetLock *sync.Mutex) {
					testLock.Unlock()
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "IsEmptySubnet", func(_ *subnetport.SubnetPortService, id string, path string) bool {
					return true
				})
				patches.ApplyMethod(reflect.TypeOf(r.BindingService), "DeleteSubnetConnectionBindingMapsByParentSubnet", func(_ *subnetbinding.BindingService, parentSubnet *model.VpcSubnet) error {
					if *parentSubnet.Id == "net1" {
						return fmt.Errorf("binding maps deletion failed")
					}
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "DeleteSubnet", func(_ *subnet.SubnetService, nsxSubnet model.VpcSubnet) error {
					if *nsxSubnet.Id == "net1" {
						require.Fail(t, "SubnetService.DeleteSubnet should not be called if binding maps are failed to delete")
					}
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "DeletePortCount", func(_ *subnetport.SubnetPortService, _ string) {})
				return patches
			},
			expHasStalePort: false,
			expErrStr:       "multiple errors occurred while deleting Subnets: [failed to delete NSX SubnetConnectionBindingMaps connected to NSX Subnet/net1: binding maps deletion failed]",
		}, {
			name:              "Succeeded to delete NSX subnet and connection binding maps",
			nsxSubnets:        nsxSubnets,
			deleteBindingMaps: true,
			patches: func(t *testing.T, r *SubnetSetReconciler) *gomonkey.Patches {
				patches := gomonkey.ApplyFunc(ctlcommon.LockSubnetSet, func(uuid types.UID) *sync.Mutex {
					testLock.Lock()
					return testLock
				})
				patches.ApplyFunc(ctlcommon.UnlockSubnetSet, func(uuid types.UID, subnetSetLock *sync.Mutex) {
					testLock.Unlock()
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "IsEmptySubnet", func(_ *subnetport.SubnetPortService, id string, path string) bool {
					return true
				})
				patches.ApplyMethod(reflect.TypeOf(r.BindingService), "DeleteSubnetConnectionBindingMapsByParentSubnet", func(_ *subnetbinding.BindingService, parentSubnet *model.VpcSubnet) error {
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetService), "DeleteSubnet", func(_ *subnet.SubnetService, nsxSubnet model.VpcSubnet) error {
					return nil
				})
				patches.ApplyMethod(reflect.TypeOf(r.SubnetPortService), "DeletePortCount", func(_ *subnetport.SubnetPortService, _ string) {})
				return patches
			},
			expHasStalePort: false,
			expErrStr:       "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &SubnetSetReconciler{
				SubnetService:     &subnet.SubnetService{},
				SubnetPortService: &subnetport.SubnetPortService{},
				BindingService:    &subnetbinding.BindingService{},
			}
			if tc.patches != nil {
				patches := tc.patches(t, r)
				defer patches.Reset()
			}

			hasPorts, err := r.deleteSubnets(tc.nsxSubnets, tc.deleteBindingMaps)
			if tc.expErrStr != "" {
				require.EqualError(t, err, tc.expErrStr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.expHasStalePort, hasPorts)
		})
	}
}

func TestSubnetSetReconciler_RestoreReconcile(t *testing.T) {
	mockCtl := gomock.NewController(t)
	k8sClient := mock_client.NewMockClient(mockCtl)
	defer mockCtl.Finish()

	r := &SubnetSetReconciler{
		Client: k8sClient,
	}

	// Reconcile success
	k8sClient.EXPECT().List(gomock.Any(), gomock.Any()).Return(nil).Do(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
		subnetSetList := list.(*v1alpha1.SubnetSetList)
		subnetSetList.Items = []v1alpha1.SubnetSet{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "subnetset-1",
					Namespace: "ns-1",
					UID:       "subnetset-1",
				},
				Status: v1alpha1.SubnetSetStatus{
					Subnets: []v1alpha1.SubnetInfo{
						{
							NetworkAddresses: []string{"10.0.0.0/28"},
							GatewayAddresses: []string{"10.0.0.0"},
						},
					},
				},
			},
		}
		return nil
	})

	patches := gomonkey.ApplyFunc((*SubnetSetReconciler).Reconcile, func(r *SubnetSetReconciler, ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		assert.Equal(t, "subnetset-1", req.Name)
		assert.Equal(t, "ns-1", req.Namespace)
		return ResultNormal, nil
	})
	defer patches.Reset()
	err := r.RestoreReconcile()
	assert.Nil(t, err)

	// Reconcile failure
	k8sClient.EXPECT().List(gomock.Any(), gomock.Any()).Return(nil).Do(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
		subnetSetList := list.(*v1alpha1.SubnetSetList)
		subnetSetList.Items = []v1alpha1.SubnetSet{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "subnetset-1",
					Namespace: "ns-1",
					UID:       "subnetset-1",
				},
				Status: v1alpha1.SubnetSetStatus{
					Subnets: []v1alpha1.SubnetInfo{
						{
							NetworkAddresses: []string{"10.0.0.0/28"},
							GatewayAddresses: []string{"10.0.0.0"},
						},
					},
				},
			},
		}
		return nil
	})
	patches = gomonkey.ApplyFunc((*SubnetSetReconciler).Reconcile, func(r *SubnetSetReconciler, ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		assert.Equal(t, "subnetset-1", req.Name)
		assert.Equal(t, "ns-1", req.Namespace)
		return ResultRequeue, nil
	})
	defer patches.Reset()
	err = r.RestoreReconcile()
	assert.Contains(t, err.Error(), "failed to restore SubnetSet ns-1/subnetset-1")
}
