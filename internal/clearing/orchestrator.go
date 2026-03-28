package clearing

import (
	"context"
	"log"
	"time"

	"github.com/erigontech/erigon-l1-bridge/internal/usecase"
)

type Orchestrator struct {
	uc       *usecase.ClearingUseCase
	interval time.Duration
}

func NewOrchestrator(uc *usecase.ClearingUseCase, interval time.Duration) *Orchestrator {
	return &Orchestrator{uc: uc, interval: interval}
}

func (o *Orchestrator) Run(ctx context.Context) {
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()
	log.Printf("clearing orchestrator started (interval=%s)", o.interval)
	for {
		select {
		case <-ctx.Done():
			log.Println("clearing orchestrator stopped")
			return
		case <-ticker.C:
			if err := o.uc.ProcessPending(ctx); err != nil {
				log.Printf("orchestrator: process pending: %v", err)
			}
		}
	}
}
