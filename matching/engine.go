package matching

import (
	logger "github.com/siddontang/go-log/log"
	"time"
)

type Engine struct {
	productId string

	OrderBook *orderBook

	orderReader OrderReader

	orderOffset int64

	orderCh chan *offsetOrder

	logStore LogStore

	logCh chan Log

	snapshotReqCh chan *Snapshot

	snapshotApproveReqCh chan *Snapshot

	snapshotCh chan *Snapshot

	snapshotStore SnapshotStore
}

type Snapshot struct {
	OrderBookSnapshot orderBookSnapshot
	OrderOffset       int64
}

type offsetOrder struct {
	Offset int64
	Order  *models.Order
}

func NewEngine(product *models.Product, orderReader OrderReader, logStore LogStore, snapshotStore SnapshotStore) *Engine {
	e := &Engine{
		productId:            product.Id,
		OrderBook:            NewOrderBook(product),
		logCh:                make(chan Log, 10000),
		orderCh:              make(chan *offsetOrder, 10000),
		snapshotReqCh:        make(chan *Snapshot, 32),
		snapshotApproveReqCh: make(chan *Snapshot, 32),
		snapshotCh:           make(chan *Snapshot, 32),
		snapshotStore:        snapshotStore,
		orderReader:          orderReader,
		logStore:             logStore,
	}

	snapshot, err := snapshotStore.GetLatest()
	if err != nil {
		logger.Fatalf("get latest snapshot error: %v", err)
	}
	if snapshot != nil {
		e.restore(snapshot)
	}
	return e
}

func (e *Engine) Start() {
	go e.runFetcher()
	go e.runApplier()
	go e.runCommitter()
	go e.runSnapshots()
}

func (e *Engine) runFetcher() {
	var offset = e.orderOffset
	if offset > 0 {
		offset = offset + 1
	}
	err := e.orderReader.SetOffset(offset)
	if err != nil {
		logger.Fatalf("set order reader offset error: %v", err)
	}

	for {
		offset, order, err := e.orderReader.FetchOrder()
		if err != nil {
			logger.Error(err)
			continue
		}
		e.orderCh <- &offsetOrder{offset, order}
	}
}

func (e *Engine) runApplier() {
	var orderOffset int64

	for {
		select {
		case offsetOrder := <-e.orderCh:
			var logs []Log
			if offsetOrder.Order.Status == models.OrderStatusCancelling {
				logs = e.OrderBook.CancelOrder(offsetOrder.Order)
			} else {
				logs = e.OrderBook.ApplyOrder(offsetOrder.Order)
			}

			for _, log := range logs {
				e.logCh <- log
			}

			orderOffset = offsetOrder.Offset

		case snapshot := <-e.snapshotReqCh:
			delta := orderOffset - snapshot.OrderOffset
			if delta <= 1000 {
				continue
			}

			logger.Infof("should take snapshot: %v %v-[%v]-%v->",
				e.productId, snapshot.OrderOffset, delta, orderOffset)

			snapshot.OrderBookSnapshot = e.OrderBook.Snapshot()
			snapshot.OrderOffset = orderOffset
			e.snapshotApproveReqCh <- snapshot
		}
	}
}

func (e *Engine) runCommitter() {
	var seq = e.OrderBook.logSeq
	var pending *Snapshot = nil
	var logs []interface{}

	for {
		select {
		case log := <-e.logCh:
			if log.GetSeq() <= seq {
				logger.Infof("discard log seq=%v", seq)
				continue
			}

			seq = log.GetSeq()
			logs = append(logs, log)

			if len(e.logCh) > 0 && len(logs) < 100 {
				continue
			}

			err := e.logStore.Store(logs)
			if err != nil {
				panic(err)
			}
			logs = nil

			if pending != nil && seq >= pending.OrderBookSnapshot.LogSeq {
				e.snapshotCh <- pending
				pending = nil
			}

		case snapshot := <-e.snapshotApproveReqCh:
			if seq >= snapshot.OrderBookSnapshot.LogSeq {
				e.snapshotCh <- snapshot
				pending = nil
				continue
			}

			if pending != nil {
				logger.Infof("discard snapshot request (seq=%v), new one (seq=%v) received",
					pending.OrderBookSnapshot.LogSeq, snapshot.OrderBookSnapshot.LogSeq)
			}
			pending = snapshot
		}
	}
}

func (e *Engine) runSnapshots() {
	orderOffset := e.orderOffset

	for {
		select {
		case <-time.After(30 * time.Second):
			e.snapshotReqCh <- &Snapshot{
				OrderOffset: orderOffset,
			}

		case snapshot := <-e.snapshotCh:
			err := e.snapshotStore.Store(snapshot)
			if err != nil {
				logger.Warnf("store snapshot failed: %v", err)
				continue
			}
			logger.Infof("new snapshot stored :product=%v OrderOffset=%v LogSeq=%v",
				e.productId, snapshot.OrderOffset, snapshot.OrderBookSnapshot.LogSeq)

			orderOffset = snapshot.OrderOffset
		}
	}
}

func (e *Engine) restore(snapshot *Snapshot) {
	logger.Infof("restoring: %+v", *snapshot)
	e.orderOffset = snapshot.OrderOffset
	e.OrderBook.Restore(&snapshot.OrderBookSnapshot)
}