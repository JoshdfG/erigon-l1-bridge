package paymaster

import (
	"context"
	"math/big"

	"github.com/erigontech/erigon-l1-bridge/internal/entity"
	"github.com/erigontech/erigon-l1-bridge/internal/usecase"
)

var _ usecase.PaymasterService = (*Service)(nil)

type Service struct {
	contractAddresses map[entity.ChainID]string
	signerKey         []byte
}

func New(signerKey []byte) *Service {
	return &Service{contractAddresses: make(map[entity.ChainID]string), signerKey: signerKey}
}

func (s *Service) RegisterChain(chain entity.ChainID, contractAddress string) {
	s.contractAddresses[chain] = contractAddress
}

func (s *Service) SponsorIntent(_ context.Context, _ entity.ClearingIntent) ([]byte, error) {
	return nil, nil
}

func (s *Service) Balance(_ context.Context, _ entity.ChainID) (*big.Int, error) {
	return big.NewInt(0), nil
}
