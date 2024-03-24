package matching

import (
	"github.com/gitbitex/gitbitex-spot/models"
)

type OrderReader interface {
	SetOffset(offset int64) error

	FetchOrder() (offset int64, order *models.Order, err error)
}

type LogStore interface {
	Store(logs []interface{}) error
}

type LogReader interface {
	GetProductId() string
	RegisterObserver(observer LogObserver)
	Run(seq, offset int64)
}

type LogObserver interface {
	OnOpenLog(log *OpenLog, offset int64)
	OnMatchLog(log *MatchLog, offset int64)
	OnDoneLog(log *DoneLog, offset int64)
}

type SnapshotStore interface {
	Store(snapshot *Snapshot) error
	GetLatest() (*Snapshot, error)
}