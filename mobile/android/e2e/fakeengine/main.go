// Command fakeengine is the engine side of the Android call test: a pairing route and a
// voice socket that speaks the same JSON frames as /api/voice/realtime, with a tone for
// Voice and counters for what the phone sent back. GET /stats on the local port reports them.
package main

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	rate      = 24000
	chunkMs   = 100
	chunksPer = 10 // one response is one second of tone
	pairCode  = "TESTCODE"
	cookie    = "dyna_device"
	token     = "fake-device-token"
)

type stats struct {
	Sockets   atomic.Int64
	MicFrames atomic.Int64
	MicBytes  atomic.Int64
	Started   atomic.Int64
	Ended     atomic.Int64
	Hangups   atomic.Int64
	Pairs     atomic.Int64
	Requests  atomic.Int64
	mu        sync.Mutex
	PlayedMs  int64
}

func tone() string {
	n := rate * chunkMs / 1000
	b := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(8000*math.Sin(2*math.Pi*440*float64(i)/rate))))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func main() {
	addr := flag.String("addr", "0.0.0.0:17457", "listen address")
	origin := flag.String("origin", "http://10.0.2.2:17457", "the Origin the app must send (the emulator's host address)")
	apk := flag.String("apk", "", "an APK to serve at /api/android/app.apk, with its version.json beside it")
	flag.Parse()
	var st stats
	audio := tone()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/pair", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Code string }
		if r.Header.Get("Origin") != *origin || r.Header.Get("X-Dyna") != "1" || json.NewDecoder(r.Body).Decode(&req) != nil || req.Code != pairCode {
			http.Error(w, `{"error":"pairing code is wrong"}`, http.StatusForbidden)
			return
		}
		st.Pairs.Add(1)
		http.SetCookie(w, &http.Cookie{Name: cookie, Value: token, Path: "/", HttpOnly: true})
		fmt.Fprint(w, `{"device":"fake","name":"fake"}`)
	})
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux.HandleFunc("/api/voice/realtime", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(cookie); err != nil || c.Value != token || r.Header.Get("Origin") != *origin {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		st.Sockets.Add(1)
		stop := make(chan struct{})
		defer close(stop)
		var wmu sync.Mutex
		send := func(v any) error {
			wmu.Lock()
			defer wmu.Unlock()
			return ws.WriteJSON(v)
		}
		go func() { // Voice speaks one second, then pauses one second, forever.
			for n := 1; ; n++ {
				id := fmt.Sprintf("r%d", n)
				for i := 0; i < chunksPer; i++ {
					if send(map[string]string{"type": "audio", "id": id, "audio": audio}) != nil {
						return
					}
					select {
					case <-stop:
						return
					case <-time.After(chunkMs * time.Millisecond):
					}
				}
				if send(map[string]string{"type": "audio_done", "id": id}) != nil {
					return
				}
				select {
				case <-stop:
					return
				case <-time.After(time.Second):
				}
			}
		}()
		for {
			var m struct {
				Type, Audio, ID, State string
				PlayedMs               int64 `json:"played_ms"`
			}
			if ws.ReadJSON(&m) != nil {
				return
			}
			switch m.Type {
			case "audio":
				st.MicFrames.Add(1)
				st.MicBytes.Add(int64(base64.StdEncoding.DecodedLen(len(m.Audio))))
			case "playback":
				if m.State == "started" {
					st.Started.Add(1)
				} else {
					st.Ended.Add(1)
					st.mu.Lock()
					st.PlayedMs += m.PlayedMs
					st.mu.Unlock()
				}
			case "hangup":
				st.Hangups.Add(1)
			}
		}
	})
	if *apk != "" {
		mux.HandleFunc("/api/android/app.apk", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, *apk) })
		mux.HandleFunc("/api/android/version", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, filepath.Join(filepath.Dir(*apk), "version.json"))
		})
	}
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		played := st.PlayedMs
		st.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]int64{"sockets": st.Sockets.Load(), "mic_frames": st.MicFrames.Load(),
			"mic_bytes": st.MicBytes.Load(), "started": st.Started.Load(), "ended": st.Ended.Load(),
			"played_ms": played, "hangups": st.Hangups.Load(), "pairs": st.Pairs.Load(), "requests": st.Requests.Load()})
	})
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stats" {
			st.Requests.Add(1)
		}
		mux.ServeHTTP(w, r)
	})
	log.Fatal(http.ListenAndServe(*addr, counted))
}
