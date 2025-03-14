package persistence

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/langgenius/dify-plugin-daemon/internal/db"
	"github.com/langgenius/dify-plugin-daemon/internal/types/models"
	"github.com/langgenius/dify-plugin-daemon/internal/utils/cache"
)

type Persistence struct {
	maxStorageSize int64

	storage PersistenceStorage
}

const (
	CACHE_KEY_PREFIX = "persistence:cache"
)

func (c *Persistence) getCacheKey(tenantId string, pluginId string, key string) string {
	return fmt.Sprintf("%s:%s:%s:%s", CACHE_KEY_PREFIX, tenantId, pluginId, key)
}

func (c *Persistence) Save(tenantId string, pluginId string, maxSize int64, key string, data []byte) error {
	if len(key) > 256 {
		return fmt.Errorf("key length must be less than 256 characters")
	}

	if maxSize == -1 {
		maxSize = c.maxStorageSize
	}

	if err := c.storage.Save(tenantId, pluginId, key, data); err != nil {
		return err
	}

	allocatedSize := int64(len(data))

	storage, err := db.GetOne[models.TenantStorage](
		db.Equal("tenant_id", tenantId),
		db.Equal("plugin_id", pluginId),
	)
	if err != nil {
		if allocatedSize > c.maxStorageSize || allocatedSize > maxSize {
			return fmt.Errorf("allocated size is greater than max storage size")
		}

		if err == db.ErrDatabaseNotFound {
			storage = models.TenantStorage{
				TenantID: tenantId,
				PluginID: pluginId,
				Size:     allocatedSize,
			}
			if err := db.Create(&storage); err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		if allocatedSize+storage.Size > maxSize || allocatedSize+storage.Size > c.maxStorageSize {
			return fmt.Errorf("allocated size is greater than max storage size")
		}

		err = db.Run(
			db.Model(&models.TenantStorage{}),
			db.Equal("tenant_id", tenantId),
			db.Equal("plugin_id", pluginId),
			db.Inc(map[string]int64{"size": allocatedSize}),
		)
		if err != nil {
			return err
		}
	}

	// delete from cache
	return cache.Del(c.getCacheKey(tenantId, pluginId, key))
}

// TODO: raises specific error to avoid confusion
func (c *Persistence) Load(tenantId string, pluginId string, key string) ([]byte, error) {
	// check if the key exists in cache
	h, err := cache.GetString(c.getCacheKey(tenantId, pluginId, key))
	if err != nil && err != cache.ErrNotFound {
		return nil, err
	}
	if err == nil {
		return hex.DecodeString(h)
	}

	// load from storage
	data, err := c.storage.Load(tenantId, pluginId, key)
	if err != nil {
		return nil, err
	}

	// add to cache
	cache.Store(c.getCacheKey(tenantId, pluginId, key), hex.EncodeToString(data), time.Minute*5)

	return data, nil
}

func (c *Persistence) Delete(tenantId string, pluginId string, key string) error {
	// delete from cache and storage
	err := cache.Del(c.getCacheKey(tenantId, pluginId, key))
	if err != nil {
		return err
	}

	// state size
	size, err := c.storage.StateSize(tenantId, pluginId, key)
	if err != nil {
		return nil
	}

	err = c.storage.Delete(tenantId, pluginId, key)
	if err != nil {
		return nil
	}

	// update storage size
	err = db.Run(
		db.Model(&models.TenantStorage{}),
		db.Equal("tenant_id", tenantId),
		db.Equal("plugin_id", pluginId),
		db.Dec(map[string]int64{"size": size}),
	)
	if err != nil {
		return err
	}

	return nil
}
