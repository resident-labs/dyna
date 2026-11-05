package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// ErrServiceHostFixed refuses a declaration that names another host than the one its service is placed on.
var ErrServiceHostFixed = errors.New("service host is fixed once placed")

// PortTakenError refuses a declaration whose host port and protocol another service of the host holds (spec 5.18).
type PortTakenError struct {
	Port    contract.PortBinding
	Service contract.ServiceName
}

func (e *PortTakenError) Error() string {
	return fmt.Sprintf("host port %d/%s is declared by service %s", e.Port.Host, e.Port.Proto, e.Service)
}

// ServiceRow is a service as the reconciler reads it. Rev counts its writes, so a write made from a stale read changes nothing.
type ServiceRow struct {
	contract.Service
	Parts      contract.ServiceParts
	ImageBytes int64
	// ImageEstimate holds pre-pull/build disk until a successful deploy replaces it with the measured ImageBytes.
	ImageEstimate int64
	UnitSHA       string
	// Target is the image reference to deploy instead of building the source, which a rollback sets. A deploy clears it.
	Target string
	// Only is the room's hosts rule, read with the row for a caller that judges placement.
	Only []string
	// Live is the declaration the running unit was written from. A rollback and a rewrite render it, so the unit and its hash stay
	// what the last good deploy made them while a newer declaration waits. Nil before the first deploy.
	Live    *contract.ServiceParts
	Rev     int64
	Updated time.Time
	Placed  time.Time
}

// Reservation is the resources and ports this service must hold until its desired declaration has successfully replaced the
// live one. The declarations are not merged: only admission uses this projection, never the unit renderer. A failed shrink or
// port change must not free resources or ports that the restored unit still uses.
func (row ServiceRow) Reservation() contract.ServiceParts {
	parts := row.Parts
	if row.Live != nil {
		parts.CPUMilli = max(parts.CPUMilli, row.Live.CPUMilli)
		parts.MemBytes = max(parts.MemBytes, row.Live.MemBytes)
		parts.DiskBytes = max(parts.DiskBytes, row.Live.DiskBytes)
		parts.Ports = slices.Clone(parts.Ports)
		for _, port := range row.Live.Ports {
			if !slices.Contains(parts.Ports, port) {
				parts.Ports = append(parts.Ports, port)
			}
		}
	}
	return parts
}

const serviceColumns = `id,room,spec,generation,host,cpu_milli,mem_bytes,disk_bytes,image_bytes,image_estimate,want,state,reason,cause,detail,last_state,deployed_generation,image_ref,previous_ref,target_ref,deployed_spec,unit_sha,restarts,since,updated_at,placed_at,rev`

type scanner interface{ Scan(...any) error }

func scanService(r scanner) (ServiceRow, error) {
	var row ServiceRow
	var spec []byte
	var live string
	var host sql.NullString
	var since, updated, placed int64
	err := r.Scan(&row.ID, &row.Room, &spec, &row.Generation, &host, &row.CPUMilli, &row.MemBytes, &row.DiskBytes, &row.ImageBytes, &row.ImageEstimate, &row.Want, &row.State, &row.Reason, &row.Cause, &row.Detail, &row.LastState, &row.Deployed, &row.ImageRef, &row.Previous, &row.Target, &live, &row.UnitSHA, &row.Restarts, &since, &updated, &placed, &row.Rev)
	if err != nil {
		return row, err
	}
	if err := json.Unmarshal(spec, &row.Spec); err != nil {
		return row, err
	}
	// The stored declaration was validated at set, so parsing it again only rebuilds the typed values.
	if row.Parts, err = row.Spec.Parse(); err != nil {
		return row, err
	}
	if live != "" {
		var liveSpec contract.ServiceSpec
		if err := json.Unmarshal([]byte(live), &liveSpec); err != nil {
			return row, err
		}
		parts, err := liveSpec.Parse()
		if err != nil {
			return row, err
		}
		row.Live = &parts
	}
	row.Host = contract.HostName(host.String)
	if since != 0 {
		row.Since = time.UnixMilli(since)
	}
	row.Updated = time.UnixMilli(updated)
	if placed != 0 {
		row.Placed = time.UnixMilli(placed)
	}
	return row, nil
}

func queryServices(ctx context.Context, q sqlQuery, where string, args ...any) ([]ServiceRow, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+serviceColumns+" FROM services WHERE "+where+" ORDER BY room,name,id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceRow
	for rows.Next() {
		row, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Services lists services, a room's or, with no room, every room's, with their secrets as set or missing.
func (s *Store) Services(ctx context.Context, room contract.RoomID) ([]ServiceRow, error) {
	where, args := "1", []any(nil)
	if room != "" {
		where, args = "room=?", []any{room}
	}
	rows, err := queryServices(ctx, s.Read(), where, args...)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Secrets, err = secretStatus(ctx, s.Read(), rows[i]); err != nil {
			return nil, err
		}
		if rows[i].Only, err = s.RoomOnly(ctx, rows[i].Room); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// Service reads one service by its stable ID, including one being removed.
func (s *Store) Service(ctx context.Context, id contract.ServiceID) (ServiceRow, error) {
	rows, err := queryServices(ctx, s.Read(), "id=?", id)
	if err != nil {
		return ServiceRow{}, err
	}
	if len(rows) == 0 {
		return ServiceRow{}, ErrNotFound
	}
	return rows[0], nil
}

// ServiceNamed finds a room's service that is not being removed.
func (s *Store) ServiceNamed(ctx context.Context, room contract.RoomID, name contract.ServiceName) (ServiceRow, error) {
	rows, err := queryServices(ctx, s.Read(), "room=? AND want!='remove' AND json_extract(spec,'$.name')=?", room, name)
	if err != nil {
		return ServiceRow{}, err
	}
	if len(rows) == 0 {
		return ServiceRow{}, fmt.Errorf("service %s: %w", name, ErrNotFound)
	}
	rows[0].Secrets, err = secretStatus(ctx, s.Read(), rows[0])
	return rows[0], err
}

func secretStatus(ctx context.Context, q sqlQuery, row ServiceRow) ([]contract.ServiceSecretStatus, error) {
	rows, err := q.QueryContext(ctx, "SELECT name FROM service_secrets WHERE service=?", row.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		set[name] = true
	}
	var out []contract.ServiceSecretStatus
	for _, name := range row.Spec.Secrets {
		out = append(out, contract.ServiceSecretStatus{Name: name, Set: set[name]})
	}
	return out, rows.Err()
}

// SetService declares a service, or changes the declaration of the room's service of that name: the generation goes up and the
// reservation covers both the desired and live declarations. A placed service keeps its host, and a host port another service of
// the host holds is refused.
func (s *Store) SetService(ctx context.Context, room contract.RoomID, parts contract.ServiceParts, id contract.ServiceID, by contract.Principal, now time.Time, newEvent func(contract.RoomID, contract.EventBody) (contract.Event, error)) (ServiceRow, error) {
	spec, err := json.Marshal(parts.Spec)
	if err != nil {
		return ServiceRow{}, err
	}
	var out ServiceRow
	err = s.Write(ctx, func(tx *sql.Tx) error {
		existing, err := queryServices(ctx, tx, "room=? AND want!='remove' AND json_extract(spec,'$.name')=?", room, parts.Spec.Name)
		if err != nil {
			return err
		}
		candidate := ServiceRow{Parts: parts}
		if len(existing) > 0 {
			candidate.Live = existing[0].Live
		}
		reservation := candidate.Reservation()
		estimate, err := imageEstimate(ctx, tx, room, parts)
		if err != nil {
			return err
		}
		host := parts.Spec.Host
		if len(existing) > 0 && existing[0].Host != "" {
			if host != "" && host != existing[0].Host {
				return fmt.Errorf("service %s is placed on %s: %w", parts.Spec.Name, existing[0].Host, ErrServiceHostFixed)
			}
			host = existing[0].Host
		}
		if host != "" {
			others, err := queryServices(ctx, tx, "(host=?1 OR json_extract(spec,'$.host')=?1) AND want!='remove'", host)
			if err != nil {
				return err
			}
			for _, other := range others {
				if len(existing) > 0 && other.ID == existing[0].ID {
					continue
				}
				for _, mine := range reservation.Ports {
					for _, theirs := range other.Reservation().Ports {
						if mine.Host == theirs.Host && mine.Proto == theirs.Proto {
							return &PortTakenError{Port: mine, Service: other.Spec.Name}
						}
					}
				}
			}
		}
		var from contract.ServiceState
		if len(existing) == 0 {
			_, err = tx.ExecContext(ctx, `INSERT INTO services(id,room,name,spec,generation,cpu_milli,mem_bytes,disk_bytes,want,state,reason,updated_at,by_kind,by_id) VALUES(?,?,?,?,1,?,?,?,?,?,'',?,?,?)`,
				id, room, parts.Spec.Name, spec, parts.CPUMilli, parts.MemBytes, parts.DiskBytes, contract.ServiceWantRun, contract.ServicePending, now.UnixMilli(), by.Kind, principalID(by))
		} else {
			id, from = existing[0].ID, existing[0].State
			_, err = tx.ExecContext(ctx, `UPDATE services SET spec=?,generation=generation+1,target_ref='',cpu_milli=?,mem_bytes=?,disk_bytes=?,image_estimate=?,updated_at=?,rev=rev+1,state=CASE WHEN state='failed' THEN 'pending' ELSE state END,reason=CASE WHEN state='failed' THEN '' ELSE reason END WHERE id=?`,
				spec, reservation.CPUMilli, reservation.MemBytes, reservation.DiskBytes, estimate, now.UnixMilli(), id)
			if err == nil {
				// A secret the declaration dropped goes with it.
				_, err = tx.ExecContext(ctx, `DELETE FROM service_secrets WHERE service=? AND name NOT IN (SELECT value FROM json_each(?))`, id, mustJSON(parts.Spec.Secrets))
			}
		}
		if err != nil {
			return err
		}
		rows, err := queryServices(ctx, tx, "id=?", id)
		if err != nil {
			return err
		}
		out = rows[0]
		if out.Secrets, err = secretStatus(ctx, tx, out); err != nil {
			return err
		}
		if len(existing) > 0 {
			return nil
		}
		event, err := newEvent(room, contract.ServiceChanged{Service: id, Name: parts.Spec.Name, From: from, To: contract.ServicePending})
		if err != nil {
			return err
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
	return out, err
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	if string(b) == "null" {
		return "[]"
	}
	return string(b)
}

func principalID(p contract.Principal) string {
	if p.Kind == contract.PrincipalSession {
		return string(p.Session)
	}
	return string(p.Device)
}

// SetServiceSecret writes a declared secret's value. Nothing reads it back except the reconciler, on its way to the host.
func (s *Store) SetServiceSecret(ctx context.Context, id contract.ServiceID, name string, value []byte, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		rows, err := queryServices(ctx, tx, "id=?", id)
		if err != nil {
			return err
		}
		if len(rows) == 0 || !slices.Contains(rows[0].Spec.Secrets, name) {
			return fmt.Errorf("service has no secret %s: %w", name, ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO service_secrets(service,name,value,set_at) VALUES(?,?,?,?) ON CONFLICT(service,name) DO UPDATE SET value=excluded.value,set_at=excluded.set_at`, id, name, value, now.UnixMilli()); err != nil {
			return err
		}
		// A new value reaches the container at the next deploy, so it counts as a new generation. The caller holds admission
		// through this write: reserve the newest source measurement before another placement can use the disk.
		estimate := rows[0].ImageEstimate
		if rows[0].Target == "" {
			estimate, err = imageEstimate(ctx, tx, rows[0].Room, rows[0].Parts)
			if err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE services SET generation=generation+1,image_estimate=?,updated_at=?,rev=rev+1 WHERE id=?`, estimate, now.UnixMilli(), id)
		return err
	})
}

// ServiceSecretValues reads a service's secret values for the step that writes them to the host as podman secrets.
func (s *Store) ServiceSecretValues(ctx context.Context, id contract.ServiceID) (map[string][]byte, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT name,value FROM service_secrets WHERE service=?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var name string
		var value []byte
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, rows.Err()
}

// SaveService writes the mutable columns of next if the row is still at old's revision, and tells whether it was. A change of
// state, reason or cause appends service.changed, and each of extra appends with it, in the same write.
func (s *Store) SaveService(ctx context.Context, old, next ServiceRow, newEvent func(contract.RoomID, contract.EventBody) (contract.Event, error), extra ...contract.EventBody) (bool, error) {
	changed := false
	err := s.Write(ctx, func(tx *sql.Tx) error {
		changed = false
		var host any
		if next.Host != "" {
			host = next.Host
		}
		placed := int64(0)
		if !next.Placed.IsZero() {
			placed = next.Placed.UnixMilli()
		}
		since := int64(0)
		if !next.Since.IsZero() {
			since = next.Since.UnixMilli()
		}
		live := ""
		if next.Live != nil {
			text, err := json.Marshal(next.Live.Spec)
			if err != nil {
				return err
			}
			live = string(text)
		}
		reservation := next.Reservation()
		result, err := tx.ExecContext(ctx, `UPDATE services SET cpu_milli=?,mem_bytes=?,disk_bytes=?,generation=?,host=?,image_bytes=?,image_estimate=?,want=?,state=?,reason=?,cause=?,detail=?,last_state=?,deployed_generation=?,image_ref=?,previous_ref=?,target_ref=?,deployed_spec=?,unit_sha=?,restarts=?,since=?,placed_at=?,updated_at=?,rev=rev+1 WHERE id=? AND rev=?`,
			reservation.CPUMilli, reservation.MemBytes, reservation.DiskBytes, next.Generation, host, next.ImageBytes, next.ImageEstimate, next.Want, next.State, next.Reason, next.Cause, next.Detail, next.LastState, next.Deployed, next.ImageRef, next.Previous, next.Target, live, next.UnitSHA, next.Restarts, since, placed, next.Updated.UnixMilli(), old.ID, old.Rev)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		changed = true
		bodies := extra
		if old.State != next.State || old.Reason != next.Reason || old.Cause != next.Cause {
			bodies = append([]contract.EventBody{contract.ServiceChanged{Service: old.ID, Name: old.Spec.Name, Host: next.Host, From: old.State, To: next.State, Reason: next.Reason, Cause: next.Cause}}, bodies...)
		}
		for _, body := range bodies {
			event, err := newEvent(old.Room, body)
			if err != nil {
				return err
			}
			if _, err := s.AppendEvent(tx, event); err != nil {
				return err
			}
		}
		return nil
	})
	return changed, err
}

// DeleteService removes a service's row and secrets once its host holds nothing of it. A host that was offline gets a debt.
func (s *Store) DeleteService(ctx context.Context, row ServiceRow, owed bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if owed {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO service_leftovers(host,room,service) VALUES(?,?,?)", row.Host, row.Room, row.ID); err != nil {
				return err
			}
		}
		for _, q := range []string{"DELETE FROM service_secrets WHERE service=?", "DELETE FROM services WHERE id=?"} {
			if _, err := tx.ExecContext(ctx, q, row.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// ServiceLeftovers lists the services a host still holds after their removal while it was offline.
func (s *Store) ServiceLeftovers(ctx context.Context, host contract.HostName) ([]contract.ServiceID, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT service FROM service_leftovers WHERE host=? ORDER BY service", host)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contract.ServiceID
	for rows.Next() {
		var id contract.ServiceID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ForgetServiceLeftover closes one debt after the host removed the service.
func (s *Store) ForgetServiceLeftover(ctx context.Context, host contract.HostName, id contract.ServiceID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM service_leftovers WHERE host=? AND service=?", host, id)
		return err
	})
}

// ServiceHolds lists what each placed service reserves, for admission and for the room's limits.
func serviceHolds(ctx context.Context, q sqlQuery) ([]box.ServiceHold, []contract.HostName, error) {
	rows, err := q.QueryContext(ctx, "SELECT id,room,host,cpu_milli,mem_bytes,disk_bytes+max(image_bytes,image_estimate) FROM services WHERE host IS NOT NULL ORDER BY id")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var holds []box.ServiceHold
	var hosts []contract.HostName
	for rows.Next() {
		var h box.ServiceHold
		var host contract.HostName
		if err := rows.Scan(&h.ID, &h.Room, &host, &h.CPUMilli, &h.MemBytes, &h.DiskBytes); err != nil {
			return nil, nil, err
		}
		holds, hosts = append(holds, h), append(hosts, host)
	}
	return holds, hosts, rows.Err()
}

// ServiceHolds lists every service that has a host, for the room's limits.
func (s *Store) ServiceHolds(ctx context.Context) ([]box.ServiceHold, error) {
	holds, _, err := serviceHolds(ctx, s.Read())
	return holds, err
}

// ImageEstimate is the newest measured size of this image on any host, or contract.ServiceImageEstimate for one never measured.
// A room image belongs to its room; a registry digest is the same image across rooms. Measurements belong to an image source,
// never a service's desired declaration, which may already name a newer image that has not been measured.
func (s *Store) ImageEstimate(ctx context.Context, room contract.RoomID, parts contract.ServiceParts) (int64, error) {
	return imageEstimate(ctx, s.Read(), room, parts)
}

func imageEstimate(ctx context.Context, q sqlQuery, room contract.RoomID, parts contract.ServiceParts) (int64, error) {
	if parts.Digest != "" {
		room = ""
	}
	var size int64
	err := q.QueryRowContext(ctx, "SELECT bytes FROM service_image_sizes WHERE room=? AND source=?", room, parts.Spec.Image).Scan(&size)
	if err == sql.ErrNoRows {
		return contract.ServiceImageEstimate, nil
	}
	return size, err
}

// RecordServiceImage records a completed pull/build's exact measurement, separately from the service's reservation (which may
// still cover a larger live image). A later failed health gate or declaration change does not change the measured image's identity.
func (s *Store) RecordServiceImage(ctx context.Context, room contract.RoomID, parts contract.ServiceParts, size int64, now time.Time) error {
	if parts.Digest != "" {
		room = ""
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO service_image_sizes(room,source,bytes,measured_at) VALUES(?,?,?,?) ON CONFLICT(room,source) DO UPDATE SET bytes=excluded.bytes,measured_at=excluded.measured_at WHERE excluded.measured_at>=service_image_sizes.measured_at`, room, parts.Spec.Image, size, now.UnixMilli())
		return err
	})
}

// RoomServices lists every service of a room, including those being removed, so the room delete waits for the last.
func (s *Store) RoomServices(ctx context.Context, room contract.RoomID) ([]ServiceRow, error) {
	return queryServices(ctx, s.Read(), "room=?", room)
}

// failServices fails the services on a host that its owner removes with the boxes discarded: they end in failed with
// host_removed, and a service already failed or removing keeps its state.
func (s *Store) failServices(ctx context.Context, tx *sql.Tx, host contract.HostName, newEvent func(contract.RoomID, contract.EventBody) (contract.Event, error)) error {
	rows, err := queryServices(ctx, tx, "host=? AND state NOT IN ('failed','removing')", host)
	if err != nil {
		return err
	}
	for _, row := range rows {
		event, err := newEvent(row.Room, contract.ServiceChanged{Service: row.ID, Name: row.Spec.Name, Host: host, From: row.State, To: contract.ServiceFailed, Reason: contract.ServiceHostRemoved})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE services SET state='failed',reason=?,cause='',updated_at=?,rev=rev+1 WHERE id=?", contract.ServiceHostRemoved, event.Time.UnixMilli(), row.ID); err != nil {
			return err
		}
		if _, err := s.AppendEvent(tx, event); err != nil {
			return err
		}
	}
	return nil
}

// ChangeService reads a service, lets change edit it and saves the result with SaveService, reading again when another write
// got in between. change returns an error to refuse. It returns the saved row.
func (s *Store) ChangeService(ctx context.Context, id contract.ServiceID, newEvent func(contract.RoomID, contract.EventBody) (contract.Event, error), change func(*ServiceRow) error) (ServiceRow, error) {
	for {
		rows, err := queryServices(ctx, s.Read(), "id=?", id)
		if err != nil {
			return ServiceRow{}, err
		}
		if len(rows) == 0 {
			return ServiceRow{}, fmt.Errorf("service %s: %w", id, ErrNotFound)
		}
		old := rows[0]
		next := old
		if err := change(&next); err != nil {
			return ServiceRow{}, err
		}
		// Owner generation changes hold admission through SaveService. A successful deploy cleared the prior estimate;
		// each new source build needs the latest measurement reserved again, not just a local preflight estimate.
		if next.Generation != old.Generation && next.Target == "" {
			next.ImageEstimate, err = s.ImageEstimate(ctx, next.Room, next.Parts)
			if err != nil {
				return ServiceRow{}, err
			}
		}
		next.Updated = time.Now()
		saved, err := s.SaveService(ctx, old, next, newEvent)
		if err != nil || saved {
			return next, err
		}
		if ctx.Err() != nil {
			return ServiceRow{}, ctx.Err()
		}
	}
}

// AddServiceLeftover records that a host may still hold what a service left.
func (s *Store) AddServiceLeftover(ctx context.Context, host contract.HostName, room contract.RoomID, id contract.ServiceID) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO service_leftovers(host,room,service) VALUES(?,?,?)", host, room, id)
		return err
	})
}
