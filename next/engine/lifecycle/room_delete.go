package lifecycle

import (
	"context"
	"fmt"
	"log"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// abandon stops waiting for a closing box on an offline host, so a deletion never hangs on it.
// The store keeps the debt, and the box's resources go when the host returns.
func (r *Reconciler) abandon(ctx context.Context, row store.LifecycleBox) error {
	event, err := r.event(row.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{})
	if err != nil {
		return err
	}
	if err := r.store.AbandonBox(ctx, row.Box, event); err != nil {
		return err
	}
	r.Kick()
	return r.roomHosts.ForgetBox(row.ID)
}

// roomPass finishes rooms whose sessions have closed and repays what hosts that returned still owe.
// One step runs per room and per host, so a slow removal never holds a pass and two passes never race on one folder.
func (r *Reconciler) roomPass(ctx context.Context, hosts []box.Host) error {
	if r.roomData == nil {
		return nil
	}
	rooms, err := r.store.RoomsReadyToDelete(ctx)
	if err != nil {
		return err
	}
	for _, room := range rooms {
		r.launchRoomStep(ctx, string(room), func(ctx context.Context) error {
			err := r.deleteRoom(ctx, room, hosts)
			if err != nil && ctx.Err() == nil {
				err = fmt.Errorf("delete room %s: %w", room, err)
				if reasonErr := r.store.SetRoomDeleteReason(ctx, room, err.Error()); reasonErr != nil {
					log.Printf("lifecycle: %v", reasonErr)
				}
			}
			return err
		})
	}
	if r.roomHosts == nil {
		return nil
	}
	for _, host := range hosts {
		if host.State != box.HostReady {
			continue
		}
		leftovers, err := r.store.Leftovers(ctx, host.Name)
		if err != nil {
			return err
		}
		if len(leftovers) > 0 {
			r.launchRoomStep(ctx, "host "+string(host.Name), func(ctx context.Context) error { return r.repay(ctx, leftovers) })
		}
	}
	return nil
}

func (r *Reconciler) launchRoomStep(ctx context.Context, key string, step func(context.Context) error) {
	r.mu.Lock()
	if r.roomSteps[key] {
		r.mu.Unlock()
		return
	}
	r.roomSteps[key] = true
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		if err := step(ctx); err != nil && ctx.Err() == nil {
			log.Printf("lifecycle: %v", err)
		}
		r.mu.Lock()
		delete(r.roomSteps, key)
		r.mu.Unlock()
	}()
}

// deleteRoom runs the steps after the boxes are gone. Every step tolerates having run before, so a restart starts it again from the top.
// A host that is not ready gets a durable debt instead of a wait; a ready host that fails holds the room, with the reason shown.
func (r *Reconciler) deleteRoom(ctx context.Context, room contract.RoomID, hosts []box.Host) error {
	if r.roomHosts != nil {
		for _, host := range hosts {
			if host.State == box.HostReady {
				if err := r.roomHosts.RemoveRoom(ctx, host.Name, room); err != nil {
					return fmt.Errorf("host %s: %w", host.Name, err)
				}
			} else if err := r.store.AddRoomLeftover(ctx, host.Name, room); err != nil {
				return err
			}
		}
	}
	if err := r.roomData.RemoveRoomData(room); err != nil {
		return err
	}
	if err := r.store.DeleteRoom(ctx, room); err != nil {
		return err
	}
	r.roomData.RoomDeleted(room)
	return nil
}

func (r *Reconciler) repay(ctx context.Context, leftovers []store.Leftover) error {
	for _, l := range leftovers {
		var err error
		if l.Box != "" {
			err = r.roomHosts.RemoveLeftoverBox(ctx, l.Host, l.Box, l.Slice)
		} else {
			err = r.roomHosts.RemoveRoom(ctx, l.Host, l.Room)
		}
		if err != nil {
			return fmt.Errorf("host %s: leftovers of room %s: %w", l.Host, l.Room, err)
		}
		if err := r.store.ForgetLeftover(ctx, l); err != nil {
			return err
		}
	}
	return nil
}
