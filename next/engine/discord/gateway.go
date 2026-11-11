package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"time"

	"github.com/coder/websocket"
)

type Message struct {
	ID      string `json:"id"`
	Channel string `json:"channel_id"`
	Guild   string `json:"guild_id"`
	Content string `json:"content"`
	Author  struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Bot      bool   `json:"bot"`
	} `json:"author"`
	Mentions    []Identity `json:"mentions"`
	Referenced  *Message   `json:"referenced_message"`
	Attachments []struct {
		URL string `json:"url"`
	} `json:"attachments"`
}

type Gateway struct {
	Client          *Client
	URL, Guild, Bot string
	Receive         func(context.Context, Message) error
}

// Run reconnects with bounded backoff. Resume state lives across connections.
func (g *Gateway) Run(ctx context.Context) {
	var session, resume string
	var seq int64
	delay := time.Second
	for ctx.Err() == nil {
		err := g.connect(ctx, &session, &resume, &seq)
		if err == nil {
			delay = time.Second
		} else {
			delay = min(delay*2, time.Minute)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (g *Gateway) connect(ctx context.Context, session, resume *string, seq *int64) error {
	endpoint := g.URL
	if *resume != "" {
		endpoint = *resume
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return errors.New("invalid Discord gateway")
	}
	q := u.Query()
	q.Set("v", "10")
	q.Set("encoding", "json")
	u.RawQuery = q.Encode()
	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: g.Client.HTTP})
	if err != nil {
		return errors.New("Discord gateway unavailable")
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)
	write := func(op int, d any) error {
		data, e := json.Marshal(struct {
			Op   int `json:"op"`
			Data any `json:"d"`
		}{op, d})
		if e != nil {
			return e
		}
		return conn.Write(ctx, websocket.MessageText, data)
	}
	type packet struct {
		Op   int             `json:"op"`
		Data json.RawMessage `json:"d"`
		Seq  *int64          `json:"s"`
		Type string          `json:"t"`
	}
	read := func() (packet, error) {
		_, data, e := conn.Read(ctx)
		var p packet
		if e == nil {
			e = json.Unmarshal(data, &p)
		}
		return p, e
	}
	hello, err := read()
	if err != nil {
		return err
	}
	var h struct {
		Interval int `json:"heartbeat_interval"`
	}
	if hello.Op != 10 || json.Unmarshal(hello.Data, &h) != nil || h.Interval <= 0 {
		return errors.New("invalid Discord hello")
	}
	if *session != "" {
		err = write(6, map[string]any{"token": g.Client.Token, "session_id": *session, "seq": *seq})
	} else {
		err = write(2, map[string]any{"token": g.Client.Token, "intents": 1 | 2 | 512 | 4096 | 32768, "properties": map[string]string{"os": "linux", "browser": "dyna", "device": "dyna"}})
	}
	if err != nil {
		return err
	}
	packets := make(chan packet)
	failed := make(chan error, 1)
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for {
			p, e := read()
			if e != nil {
				failed <- e
				return
			}
			select {
			case packets <- p:
			case <-readCtx.Done():
				return
			}
		}
	}()
	timer := time.NewTimer(time.Duration(rand.Float64()*float64(h.Interval)) * time.Millisecond)
	defer timer.Stop()
	acked := true
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-failed:
			return err
		case <-timer.C:
			if !acked {
				return errors.New("Discord heartbeat missed")
			}
			if err := write(1, *seq); err != nil {
				return err
			}
			acked = false
			timer.Reset(time.Duration(h.Interval) * time.Millisecond)
		case p := <-packets:
			if p.Seq != nil {
				*seq = *p.Seq
			}
			switch p.Op {
			case 11:
				acked = true
			case 1:
				if err := write(1, *seq); err != nil {
					return err
				}
			case 7:
				return nil
			case 9:
				var resumable bool
				_ = json.Unmarshal(p.Data, &resumable)
				if !resumable {
					*session = ""
					*resume = ""
					*seq = 0
				}
				return errors.New("Discord session invalidated")
			case 0:
				switch p.Type {
				case "READY":
					var ready struct {
						Session string `json:"session_id"`
						URL     string `json:"resume_gateway_url"`
					}
					if json.Unmarshal(p.Data, &ready) != nil {
						return errors.New("invalid Discord ready")
					}
					*session = ready.Session
					*resume = ready.URL
				case "MESSAGE_CREATE":
					var m Message
					if json.Unmarshal(p.Data, &m) != nil {
						return errors.New("invalid Discord message")
					}
					if m.Author.Bot || m.Author.ID == g.Bot {
						continue
					}
					addressed := false
					if m.Guild == g.Guild {
						for _, mention := range m.Mentions {
							if mention.ID == g.Bot {
								addressed = true
							}
						}
						if m.Referenced != nil && m.Referenced.Author.ID == g.Bot {
							addressed = true
						}
					} else if m.Guild == "" {
						member, e := g.Client.API(ctx, http.MethodGet, fmt.Sprintf("/api/v10/guilds/%s/members/%s", g.Guild, m.Author.ID), nil, "")
						addressed = e == nil && member.Status == 200
					}
					if addressed {
						if err := g.Receive(ctx, m); err != nil {
							return err
						}
					}
				}
			}
		}
	}
}
