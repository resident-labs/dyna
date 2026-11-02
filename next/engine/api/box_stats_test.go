package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type statsService struct {
	*fakeService
	reads, reports int
}

func (s *statsService) BoxStats(context.Context, contract.Principal, contract.BoxStatsRequest) (contract.BoxStatsResponse, *contract.Error) {
	s.reads++
	return contract.BoxStatsResponse{}, nil
}

func (s *statsService) BoxStatsReport(context.Context, contract.Principal, contract.BoxStatsReportRequest) (contract.BoxStatsReportResponse, *contract.Error) {
	s.reports++
	return contract.BoxStatsReportResponse{}, nil
}

// A read device can read the stats. A box cannot read them, even its own, and the owner cannot post a box's report.
func TestBoxStatsAccess(t *testing.T) {
	service := &statsService{fakeService: &fakeService{}}
	readOnly := New(service, nil).handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true, Scopes: []contract.Scope{contract.ScopeRead}})
	if got := request(readOnly, "/v1/box.stats", `{"session":"session-a"}`); got.Code != http.StatusOK {
		t.Fatalf("read device: status %d", got.Code)
	}
	if got := request(readOnly, "/v1/box.stats.report", `{"ask":1}`); got.Code != http.StatusForbidden {
		t.Fatalf("owner report: status %d", got.Code)
	}
	box := New(service, nil).handler(boxCaller())
	if got := request(box, "/v1/box.stats", `{"session":"session-a"}`); got.Code != http.StatusForbidden {
		t.Fatalf("box read: status %d", got.Code)
	}
	if got := request(box, "/v1/box.stats.report", `{"ask":1}`); got.Code != http.StatusOK {
		t.Fatalf("box report: status %d", got.Code)
	}
	if service.reads != 1 || service.reports != 1 {
		t.Fatalf("reads %d, reports %d", service.reads, service.reports)
	}
}
