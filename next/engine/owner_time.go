package engine

import (
	"context"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"strings"
	"time"
	_ "time/tzdata"
)

func (s *Service) OwnerTimezone(ctx context.Context, caller contract.Principal, req contract.OwnerTimezoneRequest) (contract.OwnerTimezoneResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.OwnerTimezoneResponse{}, forbidden()
	}
	if req.Zone != nil {
		if *req.Zone != "" {
			if *req.Zone == "Local" {
				return contract.OwnerTimezoneResponse{}, invalid("use an IANA time zone, not Local")
			}
			if _, err := time.LoadLocation(*req.Zone); err != nil {
				return contract.OwnerTimezoneResponse{}, invalid("unknown IANA time zone")
			}
		}
		if err := s.store.SetOwnerTimezone(ctx, *req.Zone); err != nil {
			return contract.OwnerTimezoneResponse{}, failure(err)
		}
	}
	zone, err := s.store.OwnerTimezone(ctx)
	if err != nil {
		return contract.OwnerTimezoneResponse{}, failure(err)
	}
	return contract.OwnerTimezoneResponse{Zone: zone}, nil
}

func (s *Service) frameClock(ctx context.Context, at, turn time.Time) (string, error) {
	format := "15:04 UTC"
	if at.UTC().Format("2006-01-02") != turn.UTC().Format("2006-01-02") {
		format = "Jan 2 15:04 UTC"
	}
	clock := at.UTC().Format(format)
	zone, err := s.store.OwnerTimezone(ctx)
	if err != nil {
		return "", err
	}
	if zone != "" {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return "", err
		}
		clock += " (" + at.In(loc).Format("15:04") + " owner time)"
	}
	return clock, nil
}

func (s *Service) inputFrame(ctx context.Context, viewer contract.SessionID, event contract.Event, delivery contract.Delivery) (string, []contract.TurnFile, error) {
	return s.inputFrameAt(ctx, viewer, event, s.now(), delivery)
}
func (s *Service) inputFrameAt(ctx context.Context, viewer contract.SessionID, event contract.Event, turn time.Time, delivery contract.Delivery) (string, []contract.TurnFile, error) {
	frame, files, err := s.inputFrameBody(ctx, viewer, event, delivery)
	if err != nil {
		return "", nil, err
	}
	clock, err := s.frameClock(ctx, event.Time, turn)
	if err != nil {
		return "", nil, err
	}
	frame = strings.Replace(frame, "[Dyna] ", "[Dyna] "+clock+". ", 1)
	owner := event.From.Kind == contract.PrincipalOwner
	if discord, ok := event.Body.(contract.DiscordMessage); ok {
		owner = discord.Owner
	}
	if owner {
		previous, err := s.store.PreviousOwnerInput(ctx, viewer, event.Seq)
		if err != nil {
			return "", nil, err
		}
		gap := event.Time.Sub(previous)
		if !previous.IsZero() && gap > 10*time.Minute {
			minutes := int(gap / time.Minute)
			duration := fmt.Sprintf("%d m", minutes)
			if minutes >= 60 {
				duration = fmt.Sprintf("%d h %d m", minutes/60, minutes%60)
			}
			// Keep all metadata on the first line, including approval frames with a second line.
			first, rest, split := strings.Cut(frame, "\n")
			frame = first + " The owner's previous message was " + duration + " ago."
			if split {
				frame += "\n" + rest
			}
		}
	}
	return frame, files, nil
}
