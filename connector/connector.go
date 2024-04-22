package connector

import (
	"context"
	"fmt"
	"github.com/k0marov/tonconnect"
	"sync"
)

type Connector struct {
	session *tonconnect.Session
	storage Storage
}

func GetConnector(storage Storage) (*Connector, error) {
	var session *tonconnect.Session
	if existingSession, err := storage.Get(); err == nil {
		session = existingSession
	} else {
		session, err = tonconnect.NewSession()
		if err != nil {
			return nil, fmt.Errorf("failed creating new session: %w", err)
		}
		if err := storage.Set(session); err != nil {
			return nil, fmt.Errorf("failed setting session in storage: %w", err)
		}
	}
	return &Connector{
		session: session,
		storage: storage,
	}, nil
}
func (c *Connector) SendTransaction(ctx context.Context, tx tonconnect.Transaction, options ...tonconnect.BridgeMessageOption) ([]byte, error) {
	resp, err := c.session.SendTransaction(ctx, tx, options...)
	if err != nil {
		return nil, err
	}
	if err := c.storage.Set(c.session); err != nil {
		return nil, fmt.Errorf("saving session after sucessful SendTransaction: %w", err)
	}
	return resp, nil
}

//	func (c *Connector) Connect(ctx context.Context, wallets ...tonconnect.Wallet) (*tonconnect.ConnectResponse, error) {
//		resp, err := c.session.Connect(ctx, wallets...)
//		if err != nil {
//			return nil, err
//		}
//		if err := c.storage.Set(c.session); err != nil {
//			return nil, fmt.Errorf("saving session after sucessful Connect: %w", err)
//		}
//		return resp, nil
//	}
func (c *Connector) Connect(ctx context.Context, wallets ...tonconnect.Wallet) (*tonconnect.ConnectResponse, error) {
	var wg sync.WaitGroup
	respCh := make(chan *tonconnect.ConnectResponse)
	errCh := make(chan error, len(wallets))

	for _, wallet := range wallets {
		wg.Add(1)
		go func(w tonconnect.Wallet) {
			defer wg.Done()
			resp, err := c.session.Connect(ctx, w)
			if err != nil {
				errCh <- err
			} else {
				select {
				case respCh <- resp:
					return
				default:
				}
			}
		}(wallet)
	}

	go func() {
		wg.Wait()
		close(respCh)
		close(errCh)
	}()

	for {
		select {
		case resp := <-respCh:
			if resp != nil {
				if err := c.storage.Set(c.session); err != nil {
					return nil, fmt.Errorf("saving session after successful Connect: %w", err)
				}
				return resp, nil
			}
		case _, ok := <-errCh:
			if !ok {
				return nil, fmt.Errorf("all connect attempts failed")
			}
		}
	}
}

func (c *Connector) Disconnect(ctx context.Context, options ...tonconnect.BridgeMessageOption) error {
	err := c.session.Disconnect(ctx, options...)
	if err != nil {
		return err
	}
	if err := c.storage.Set(c.session); err != nil {
		return fmt.Errorf("saving session after sucessful Disconnect: %w", err)
	}
	return nil
}

func (c *Connector) SignData(ctx context.Context, data tonconnect.SignData, options ...tonconnect.BridgeMessageOption) (*tonconnect.SignDataResult, error) {
	resp, err := c.session.SignData(ctx, data, options...)
	if err != nil {
		return nil, err
	}
	if err := c.storage.Set(c.session); err != nil {
		return nil, fmt.Errorf("saving session after sucessful SignData: %w", err)
	}
	return resp, nil
}

func (c *Connector) GenerateUniversalLink(wallet tonconnect.Wallet, connreq tonconnect.ConnectRequest, options ...tonconnect.LinkOption) (string, error) {
	return c.session.GenerateUniversalLink(wallet, connreq, options...)
}
