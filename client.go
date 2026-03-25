package main

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// If the user is disconnected for this long, treat it as if they went offline.
	stayOnlinePeriod = 30 * time.Second

	// Maximum message size allowed from peer.
	maxMessageSize = 512
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func readPump(ctx context.Context, conn *websocket.Conn, userID, friendID string) {
	defer func() {
		_ = conn.Close()
	}()
	conn.SetReadLimit(maxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(pongWait)) })
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}

		err = rdb.Publish(ctx, friendID, message).Err()
		if err != nil {
			_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
			log.Printf("error: %v", err)
			break
		}

		// Write back to yourself
		_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
		w, err := conn.NextWriter(websocket.TextMessage)
		if err != nil {
			return
		}
		_, _ = w.Write(fmt.Appendf(nil, "%s: %s", userID, message))
		if err := w.Close(); err != nil {
			return
		}
	}
}

func writePump(ctx context.Context, conn *websocket.Conn, userID, friendID string) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = conn.Close()
	}()

	pubsub := rdb.Subscribe(ctx, userID)
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-pubsub.Channel():
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			_, _ = w.Write(fmt.Appendf(nil, "%s: %s", friendID, message.Payload))
			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// serveWs handles websocket requests from the peer.
func serveWs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	q := r.URL.Query()
	userID := q.Get("from")
	friendID := q.Get("to")
	userAgent := "web"

	// Avoid using r.Context(), might have issue with flusher etc.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Go(func() {
		// Received from your friend, write to yourself.
		writePump(ctx, conn, userID, friendID)
	})
	wg.Go(func() {
		defer cancel()
		// Read from yourself, send to your friend.
		readPump(ctx, conn, userID, friendID)
	})
	wg.Go(func() {
		keepOnline(ctx, userID, userAgent)
	})
	wg.Wait()
}

func keepOnline(ctx context.Context, userID string, userAgent string) {
	t := time.NewTicker((stayOnlinePeriod * 9) / 10)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			key := Key("users", userID)
			val := map[string]any{
				userAgent: "session-id",
				// web: session-id,
				// ios: session-id,
				// android: session-id,
			}

			// If you do not need to support multiple devices, just use SET.
			err := cmp.Or(
				rdb.HSet(ctx, key, val).Err(),
				rdb.HExpire(ctx, key, stayOnlinePeriod, userAgent).Err(),
			)
			if err != nil {
				log.Fatalf("rdb: hash error: %v", err)
			}
		}
	}
}

func Key(ss ...string) string {
	return strings.Join(ss, ":")
}
