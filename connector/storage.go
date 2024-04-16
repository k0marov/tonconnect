package connector

import "github.com/k0marov/tonconnect"

type Storage interface {
	Get() (*tonconnect.Session, error)
	Set(session *tonconnect.Session) error
}
