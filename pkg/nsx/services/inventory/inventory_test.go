package inventory

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	commonservice "github.com/vmware-tanzu/nsx-operator/pkg/nsx/services/common"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/stretchr/testify/assert"
	nsxt "github.com/vmware/go-vmware-nsxt"
	"github.com/vmware/go-vmware-nsxt/containerinventory"
)

func TestInitializeService(t *testing.T) {
	// Initialize the service
	service := commonservice.Service{}
	inventoryService := NewInventoryService(service)
	patches := gomonkey.ApplyMethod(inventoryService, "Initialize", func(*InventoryService) error {
		return nil
	})
	defer patches.Reset()
	_, err := InitializeService(service)
	assert.Nil(t, err)
}

func TestInventoryService_initContainerCluster(t *testing.T) {
	inventoryService, _ := createService(t)

	t.Run("GetContainerCluster succ", func(t *testing.T) {
		patches := gomonkey.ApplyMethod(inventoryService, "GetContainerCluster", func(*InventoryService) (containerinventory.ContainerCluster, error) {
			return containerinventory.ContainerCluster{}, nil
		})
		err := inventoryService.initContainerCluster()
		patches.Reset()
		assert.Nil(t, err)
	})

	t.Run("GetContainerCluster failed, AddContainerCluster succ", func(t *testing.T) {
		patches := gomonkey.ApplyMethod(inventoryService, "GetContainerCluster", func(*InventoryService) (containerinventory.ContainerCluster, error) {
			return containerinventory.ContainerCluster{}, errors.New("get error")
		})
		patches.ApplyMethod(inventoryService, "AddContainerCluster", func(_ *InventoryService, _ containerinventory.ContainerCluster) (containerinventory.ContainerCluster, error) {
			return containerinventory.ContainerCluster{}, nil
		})
		err := inventoryService.initContainerCluster()
		patches.Reset()
		assert.Nil(t, err)
	})

	t.Run(" GetContainerCluster failed, AddContainerCluster failed", func(t *testing.T) {
		createErr := errors.New("get error")
		patches := gomonkey.ApplyMethod(inventoryService, "GetContainerCluster", func(*InventoryService) (containerinventory.ContainerCluster, error) {
			return containerinventory.ContainerCluster{}, errors.New("get error")
		})
		patches.ApplyMethod(inventoryService, "AddContainerCluster", func(_ *InventoryService, _ containerinventory.ContainerCluster) (containerinventory.ContainerCluster, error) {
			return containerinventory.ContainerCluster{}, createErr
		})
		err := inventoryService.initContainerCluster()
		patches.Reset()
		assert.Equal(t, err, createErr)
	})
}

func TestInventoryService_SyncInventoryObject(t *testing.T) {
	// Setup
	inventoryService, _ := createService(t)

	t.Run("Empty bufferedKeys", func(t *testing.T) {
		bufferedKeys := emptyKeySet
		retryKeys, err := inventoryService.SyncInventoryObject(bufferedKeys)
		assert.Empty(t, retryKeys)
		assert.NoError(t, err)
	})

	t.Run("Valid ContainerApplicationInstance key", func(t *testing.T) {
		key := InventoryKey{Key: "namespace/name", InventoryType: ContainerApplicationInstance}
		bufferedKeys := emptyKeySet
		bufferedKeys.Insert(key)
		patches := gomonkey.ApplyMethod(reflect.TypeOf(inventoryService), "SyncContainerApplicationInstance", func(s *InventoryService, name string, namespace string, key InventoryKey) *InventoryKey {
			return nil
		})
		defer patches.Reset()
		retryKeys, err := inventoryService.SyncInventoryObject(bufferedKeys)
		assert.Empty(t, retryKeys)
		assert.NoError(t, err)
	})

	t.Run("ContainerApplicationInstance key with sync failure", func(t *testing.T) {
		key := InventoryKey{Key: "namespace/name", InventoryType: ContainerApplicationInstance}
		bufferedKeys := emptyKeySet
		bufferedKeys.Insert(key)

		retryKey := InventoryKey{Key: "namespace/name", InventoryType: ContainerApplicationInstance}
		patches := gomonkey.ApplyMethod(reflect.TypeOf(inventoryService), "SyncContainerApplicationInstance", func(s *InventoryService, name string, namespace string, key InventoryKey) *InventoryKey {
			return &retryKey
		})
		defer patches.Reset()
		retryKeys, err := inventoryService.SyncInventoryObject(bufferedKeys)
		assert.Contains(t, retryKeys, retryKey)
		assert.NoError(t, err)
	})

	t.Run("NSX request failure", func(t *testing.T) {
		key := InventoryKey{Key: "namespace/name", InventoryType: ContainerApplicationInstance}
		bufferedKeys := emptyKeySet
		bufferedKeys.Insert(key)

		patches := gomonkey.ApplyMethod(reflect.TypeOf(inventoryService), "SyncContainerApplicationInstance", func(s *InventoryService, name string, namespace string, key InventoryKey) *InventoryKey {
			return nil
		})
		patches.ApplyPrivateMethod(reflect.TypeOf(inventoryService), "sendNSXRequestAndUpdateInventoryStore", func(s *InventoryService) error {
			return errors.New("NSX request failed")
		})
		defer patches.Reset()
		retryKeys, err := inventoryService.SyncInventoryObject(bufferedKeys)
		assert.Equal(t, bufferedKeys, retryKeys)
		assert.Error(t, err)
	})
}

func TestInventoryService_DeleteResource(t *testing.T) {
	inventoryService, _ := createService(t)

	t.Run("ResourceExists", func(t *testing.T) {
		externalId := "existing-id"
		appInstance1 := containerinventory.ContainerApplicationInstance{
			DisplayName:        "test",
			ResourceType:       string(ContainerApplicationInstance),
			ExternalId:         externalId,
			ContainerProjectId: "qe",
		}
		inventoryService.ApplicationInstanceStore.Add(&appInstance1)
		err := inventoryService.DeleteResource("existing-id", ContainerApplicationInstance)

		assert.Nil(t, err)
		assert.Equal(t, 1, len(inventoryService.requestBuffer))
		assert.Equal(t, 1, len(inventoryService.pendingDelete))
		obj := inventoryService.requestBuffer[0].ContainerObject
		assert.Equal(t, externalId, obj["external_id"])
		assert.Equal(t, ContainerApplicationInstance, obj["resource_type"])
		assert.Equal(t, "qe", inventoryService.pendingDelete[externalId].(*containerinventory.ContainerApplicationInstance).ContainerProjectId)

		//clean
		inventoryService.requestBuffer = make([]containerinventory.ContainerInventoryObject, 0)
		delete(inventoryService.pendingDelete, externalId)
	})

	t.Run("ResourceNotExists", func(t *testing.T) {
		err := inventoryService.DeleteResource("non-existing-id", ContainerApplicationInstance)
		assert.Nil(t, err)
		assert.Equal(t, 0, len(inventoryService.requestBuffer))
		assert.Equal(t, 0, len(inventoryService.pendingDelete))
	})

	t.Run("UnknownResourceType", func(t *testing.T) {
		err := inventoryService.DeleteResource("some-id", "UnknownType")
		assert.NotNil(t, err)
		assert.Equal(t, "unknown resource_type: UnknownType for external_id some-id", err.Error())
	})

	t.Run("DeleteExistingContainerProject", func(t *testing.T) {
		externalId := "existing-project-id"
		project := containerinventory.ContainerProject{
			DisplayName:  "test-project",
			ResourceType: string(ContainerProject),
			ExternalId:   externalId,
		}
		inventoryService.ProjectStore.Add(&project)
		err := inventoryService.DeleteResource(externalId, ContainerProject)

		assert.Nil(t, err)
		assert.Equal(t, 1, len(inventoryService.requestBuffer))
		assert.Equal(t, 1, len(inventoryService.pendingDelete))
		obj := inventoryService.requestBuffer[0].ContainerObject
		assert.Equal(t, externalId, obj["external_id"])
		assert.Equal(t, ContainerProject, obj["resource_type"])
		assert.Equal(t, "test-project", inventoryService.pendingDelete[externalId].(*containerinventory.ContainerProject).DisplayName)

		// Clean up
		inventoryService.requestBuffer = make([]containerinventory.ContainerInventoryObject, 0)
		delete(inventoryService.pendingDelete, externalId)
	})
}

func TestInventoryService_sendNSXRequestAndUpdateInventoryStore(t *testing.T) {
	clusterApiService := &nsxt.ManagementPlaneApiFabricContainerInventoryApiService{}
	appInstance1 := containerinventory.ContainerApplicationInstance{
		DisplayName:  "test",
		ResourceType: string(ContainerApplicationInstance),
		ExternalId:   "application1",
	}
	inventoryService, _ := createService(t)
	patches := gomonkey.ApplyMethod(reflect.TypeOf(clusterApiService), "AddContainerInventoryUpdateUpdates", func(_ *nsxt.ManagementPlaneApiFabricContainerInventoryApiService, _ context.Context, _ string, _ containerinventory.ContainerInventoryData) (*http.Response, error) {
		return &http.Response{StatusCode: 200}, nil
	})
	defer patches.Reset()
	inventoryService.pendingAdd["application1"] = &appInstance1
	inventoryObj := containerinventory.ContainerInventoryObject{}
	inventoryService.requestBuffer = []containerinventory.ContainerInventoryObject{inventoryObj}
	err := inventoryService.sendNSXRequestAndUpdateInventoryStore()
	assert.Nil(t, err)
	itemNum := len(inventoryService.ApplicationInstanceStore.List())
	assert.Equal(t, 1, itemNum, "expected 1 item in the inventory, got %d", itemNum)
}

func TestInventoryService_updateInventoryStore(t *testing.T) {
	service, _ := createService(t)

	appInstance1 := containerinventory.ContainerApplicationInstance{
		DisplayName:  "test",
		ResourceType: string(ContainerApplicationInstance),
		ExternalId:   "application1",
	}

	t.Run("Add  existing ContainerApplicationInstance", func(t *testing.T) {
		service.pendingAdd["application1"] = &appInstance1
		err := service.updateInventoryStore()
		if err != nil {
			t.Errorf("Add ContainerApplicationInstance failed: %v", err)
		}
		itemNum := len(service.ApplicationInstanceStore.List())
		assert.Equal(t, 1, itemNum, "expected 1 item in the inventory, got %d", itemNum)
	})

	t.Run("Deleting  existing ContainerApplicationInstance", func(t *testing.T) {
		delete(service.pendingAdd, "application1")
		service.pendingDelete["application1"] = &appInstance1
		err := service.updateInventoryStore()
		if err != nil {
			t.Errorf("Delete ContainerApplicationInstance failed: %v", err)
		}
		itemNum := len(service.ApplicationInstanceStore.List())
		assert.Equal(t, 0, itemNum, "expected 0 item in the inventory, got %d", itemNum)
	})

	t.Run("Deleting non-existing ContainerApplicationInstance", func(t *testing.T) {
		err := service.updateInventoryStore()
		if err != nil {
			t.Errorf("Delete ContainerApplicationInstance failed: %v", err)
		}
		itemNum := len(service.ApplicationInstanceStore.List())
		assert.Equal(t, 0, itemNum, "expected 0 item in the inventory, got %d", itemNum)
	})

	project1 := containerinventory.ContainerProject{
		DisplayName:  "test-project",
		ResourceType: string(ContainerProject),
		ExternalId:   "project1",
	}

	t.Run("Add new ContainerProject", func(t *testing.T) {
		service.pendingAdd["project1"] = &project1
		err := service.updateInventoryStore()
		assert.NoError(t, err)
		itemNum := len(service.ProjectStore.List())
		assert.Equal(t, 1, itemNum, "expected 1 item in the project inventory, got %d", itemNum)
	})

	t.Run("Delete existing ContainerProject", func(t *testing.T) {
		delete(service.pendingAdd, "project1")
		service.pendingDelete["project1"] = &project1
		err := service.updateInventoryStore()
		assert.NoError(t, err)
		itemNum := len(service.ProjectStore.List())
		assert.Equal(t, 0, itemNum, "expected 0 items in the project inventory, got %d", itemNum)
	})

	t.Run("Delete non-existing ContainerProject", func(t *testing.T) {
		nonExistingProject := containerinventory.ContainerProject{
			DisplayName:  "non-existing",
			ResourceType: string(ContainerProject),
			ExternalId:   "non-existing",
		}
		service.pendingDelete["non-existing"] = &nonExistingProject
		err := service.updateInventoryStore()
		assert.NoError(t, err)
		itemNum := len(service.ProjectStore.List())
		assert.Equal(t, 0, itemNum, "expected 0 items in the project inventory, got %d", itemNum)
	})
}
