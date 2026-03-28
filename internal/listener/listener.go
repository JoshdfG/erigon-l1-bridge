package listener

import (
	"context"
	"fmt"
	"log"

	"github.com/erigontech/erigon-l1-bridge/internal/entity"
	"github.com/erigontech/erigon-l1-bridge/internal/usecase"
)

type EventIngester interface {
	IngestEvent(ctx context.Context, event entity.BridgeEvent) error
}

type Listener struct {
	clients  map[entity.ChainID]usecase.ChainClient
	ingester EventIngester
}

func New(ingester EventIngester) *Listener {
	return &Listener{clients: make(map[entity.ChainID]usecase.ChainClient), ingester: ingester}
}

func (l *Listener) Register(client usecase.ChainClient) {
	l.clients[client.ChainID()] = client
}

func (l *Listener) Start(ctx context.Context) error {
	if len(l.clients) == 0 {
		return fmt.Errorf("listener: no chains registered")
	}
	for _, client := range l.clients {
		c := client
		go func() {
			if err := c.SubscribeLogs(ctx, func(event entity.BridgeEvent) {
				if err := l.ingester.IngestEvent(ctx, event); err != nil {
					log.Printf("listener: ingest chain %d: %v", event.SourceChain, err)
				}
			}); err != nil {
				log.Printf("listener: subscribe chain %d: %v", c.ChainID(), err)
			}
		}()
	}
	return nil
}
