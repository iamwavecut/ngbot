package reg

import (
	"github.com/iamwavecut/ngbot/internal/db"
	"sync"
)

type (
	registry struct {
		cmCache map[int64]*db.ChatMeta
		umCache map[int]*db.UserMeta
	}
)

var instance *registry
var once sync.Once

func Get() *registry {
	once.Do(func() {
		instance = &registry{
			cmCache: map[int64]*db.ChatMeta{},
			umCache: map[int]*db.UserMeta{},
		}
	})
	return instance
}

func (r *registry) GetCM(ID int64) *db.ChatMeta {
	return r.cmCache[ID]
}
func (r *registry) SetCM(cm *db.ChatMeta) {
	r.cmCache[cm.ID] = cm
}
func (r *registry) RemoveCM(ID int64) {
	delete(r.cmCache, ID)
}

func (r *registry) GetUM(ID int) *db.UserMeta {
	return r.umCache[ID]
}
func (r *registry) SetUM(um *db.UserMeta) {
	r.umCache[um.ID] = um
}
func (r *registry) RemoveUM(ID int) {
	delete(r.umCache, ID)
}
