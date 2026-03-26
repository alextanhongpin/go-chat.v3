package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

var ErrClosed = errors.New("chat: write closed")

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

type wsServer struct {
	rdb *redis.Client
}

func (svr *wsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	defer func() {
		_ = conn.Close()
	}()

	// authorize
	// validate
	// create chat

	id := r.URL.Query().Get("chat_id")
	userID := r.URL.Query().Get("user_id")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chat := NewChat[any](conn, id)

	var wg sync.WaitGroup
	// Write back to websocket.
	// Subscribe to redis pubsub.
	wg.Go(func() {
		pubsub := svr.rdb.Subscribe(ctx, id)
		defer func() {
			_ = pubsub.Close()
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-pubsub.Channel():
				err = chat.Write(ctx, []byte(msg.Payload))
				if err != nil {
					return
				}
			}
		}
	})
	// TODO: Add online presence indicator.
	// wg.Go(func() { stayOnline() })

	// Read websocket messages. Blocking read.
	chat.Read(func(msg []byte) error {
		return svr.rdb.Publish(ctx, id, fmt.Appendf(nil, "%s: %s", userID, msg)).Err()
	})
	cancel()
	wg.Wait()
	log.Println("disconnected from", id)
}

type Chat[T any] struct {
	ch   chan []byte
	conn *websocket.Conn
	done chan struct{}
	id   string
	once sync.Once
}

func NewChat[T any](conn *websocket.Conn, id string) *Chat[T] {
	return &Chat[T]{
		ch:   make(chan []byte),
		conn: conn,
		done: make(chan struct{}),
		id:   id,
	}
}

func (c *Chat[T]) WriteJSON(v T) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}

	select {
	case <-c.done:
		return ErrClosed
	case c.ch <- b:
		return nil
	}
}

func (c *Chat[T]) Write(ctx context.Context, msg []byte) error {
	select {
	case <-c.done:
		return ErrClosed
	case c.ch <- msg:
		return nil
	}
}

func (c *Chat[T]) writer() {
	conn := c.conn

	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = conn.Close()
	}()

	for {
		select {
		case <-c.done:
			return
			// TODO: Instead of waiting for the message, we can buffer the message
			// and flush it periodically, e.g. every 100ms.
		case message, ok := <-c.ch:
			if !ok {
				_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
				_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			_ = conn.WriteMessage(websocket.TextMessage, message)
			/*
				Use this for large payload.
				_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
				w, err := conn.NextWriter(websocket.TextMessage)
				if err != nil {
					return
				}
				_, _ = w.Write(msg)
				if err := w.Close(); err != nil {
					return
				}
			*/
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Chat[T]) ReadJSON(fn func(T) error) {
	defer c.stop()

	var wg sync.WaitGroup
	wg.Go(c.writer)

	conn := c.conn
	conn.SetReadLimit(maxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		var t T
		err := conn.ReadJSON(&t)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
		if err := fn(t); err != nil {
			log.Printf("error: %v", err)
			_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
			break
		}
	}

	// Stop writer.
	c.stop()
	wg.Wait()
}

func (c *Chat[T]) Read(fn func([]byte) error) {
	defer c.stop()

	var wg sync.WaitGroup
	wg.Go(c.writer)

	conn := c.conn
	conn.SetReadLimit(maxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	// Block reads until disconnected.
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
		if err := fn(message); err != nil {
			log.Printf("error: %v", err)
			_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
			break
		}
	}

	// Stop writer.
	c.stop()
	wg.Wait()
}

func (c *Chat[T]) stop() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

func keepOnline(ctx context.Context, userID string, userAgent string) {
	t := time.NewTicker((stayOnlinePeriod * 9) / 10)
	defer t.Stop()

	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379", // use default address
		Password: "",               // no password set
		DB:       0,                // use default DB
	})

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			key := fmt.Sprintf("users:%s", userID)
			val := map[string]any{
				userAgent: "chat-id",
				// web: chat-id,
				// ios: chat-id,
				// android: chat-id,
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
