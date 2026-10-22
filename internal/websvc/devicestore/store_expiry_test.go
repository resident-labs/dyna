package devicestore

import (
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestIssueDeviceExpiresEarlyAndKeepsOwnerCode(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.now = func() time.Time { return now }
	code, err := s.NewCode("")
	if err != nil {
		t.Fatal(err)
	}
	token, dev, err := s.IssueDevice("", "short", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !dev.Expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("expires %s", dev.Expires)
	}
	if _, ok := s.Check(token); !ok {
		t.Fatal("new device rejected")
	}
	if _, _, err := s.Pair(code, "owner phone"); err != nil {
		t.Fatalf("issuing a device cancelled the owner's pairing code: %v", err)
	}
	now = now.Add(time.Hour + time.Second)
	if _, ok := s.Check(token); ok {
		t.Fatal("expired device accepted")
	}
}

func TestIssueDeviceConcurrentlyKeepsEveryDevice(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	tokens := make([]string, 8)
	for i := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if tokens[i], _, err = s.IssueDevice("browser-"+strconv.Itoa(i), "b", time.Hour); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for i, token := range tokens {
		if _, ok := s.Check(token); !ok {
			t.Errorf("device %d lost", i)
		}
	}
}

func TestADeviceInUseRenewsItsExpiry(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.now = func() time.Time { return now }
	code, err := s.NewCode("")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.Pair(code, "owner browser")
	if err != nil {
		t.Fatal(err)
	}
	// Twenty-nine days of use: the device is days from its end, so the
	// expiry slides forward a full DeviceLife.
	now = now.Add(29 * 24 * time.Hour)
	if _, ok := s.Check(token); !ok {
		t.Fatal("a live device was rejected")
	}
	// The slide kept it alive past its original expiry.
	now = now.Add(2 * 24 * time.Hour)
	if _, ok := s.Check(token); !ok {
		t.Fatal("a device in use expired; its expiry did not slide")
	}
	// Another month of use slides it again: renewal is not once only.
	now = now.Add(29 * 24 * time.Hour)
	if _, ok := s.Check(token); !ok {
		t.Fatal("a renewed device expired; the second slide failed")
	}
	list, _ := s.Devices()
	if len(list) != 1 || !list[0].Expires.After(now.Add(DeviceLife-RenewEvery)) {
		t.Fatalf("expiry did not renew: %+v", list)
	}
}

// The store must read back its own writes even when the filesystem keeps
// the file's (size, mtime) stamp unchanged across them: pin the stamp a
// read recorded, renew, and require the next read to see the renewal.
func TestTheStoreReadsBackItsOwnWrites(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.now = func() time.Time { return now }
	code, err := s.NewCode("")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.Pair(code, "owner browser")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Check(token); !ok {
		t.Fatal("a live device was rejected")
	}
	fi, err := os.Stat(s.path("devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	stamp := fi.ModTime()
	// Twenty-nine days of use renews the device and rewrites the file.
	now = now.Add(29 * 24 * time.Hour)
	if _, ok := s.Check(token); !ok {
		t.Fatal("a live device was rejected")
	}
	// The rewrite kept every timestamp the same length, so only mtime
	// could tell the two files apart: force it not to.
	if err := os.Chtimes(s.path("devices.json"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * 24 * time.Hour)
	if _, ok := s.Check(token); !ok {
		t.Fatal("a device in use expired: the store did not read back its renewal write")
	}
	list, _ := s.Devices()
	if len(list) != 1 || !list[0].Expires.After(now.Add(DeviceLife-RenewEvery)) {
		t.Fatalf("expiry did not renew: %+v", list)
	}
}
