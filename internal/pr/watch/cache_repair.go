package watch

import (
	"context"
	"log"
	"strings"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/room"
)

// CacheRepairReporter returns the callback a local CI run uses to report a
// cache repair. The run posts one typed machine event to the room whose CI
// triggered the repair, so the owner reads the fact where their work ran.
func (s *Service) CacheRepairReporter(roomID string) localci.CacheRepairReporter {
	return func(_ context.Context, repair localci.CacheRepair) {
		s.postCacheRepair(roomID, repair)
	}
}

// postCacheRepair posts one machine cache-repair event. The post is local and
// quick, so it does not depend on the run's own context or outcome: a repair
// is a fact even when the run fails afterwards. The event names the machine and
// the cache kinds in words, never a path or a link target.
func (s *Service) postCacheRepair(roomID string, repair localci.CacheRepair) {
	if roomID == "" || len(repair.Kinds) == 0 {
		return
	}
	rec, err := s.roomsStore().Resolve(roomID)
	if err != nil {
		log.Printf("cache repair on %s: resolve room: %v", repair.Node, err)
		return
	}
	caches := make([]string, 0, len(repair.Kinds))
	for _, kind := range repair.Kinds {
		caches = append(caches, cacheKindLabel(kind))
	}
	ev := room.MachineCacheEvent{
		Name:    repair.Node,
		Caches:  caches,
		Summary: strings.Join(caches, " and "),
		AtMS:    int(s.now().UnixMilli()),
	}
	if _, err := s.roomsStore().PostEventInput(room.PostEventInput{
		RoomID: rec.ID, Event: ev, To: []string{rec.Name}, RoomService: true,
	}); err != nil {
		log.Printf("cache repair on %s: post event: %v", repair.Node, err)
	}
}

// cacheKindLabel names one managed cache kind in words a person reads.
func cacheKindLabel(kind localci.CacheKind) string {
	switch kind {
	case localci.CacheGoBuild:
		return "Go build cache"
	case localci.CacheGoMod:
		return "Go module cache"
	default:
		return string(kind)
	}
}
