package plugin

import (
	"fmt"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	kdb "github.com/sv/kdbgo"
)

// wrappers for correct run-time evaluation of KdbHandle pointer and to enable unit testing
func (d *KdbDatasource) writeMessage(msgtype kdb.ReqType, obj *kdb.K) error {
	return d.KdbHandle.WriteMessage(msgtype, obj)
}

func (d *KdbDatasource) readMessage() (*kdb.K, kdb.ReqType, error) {
	return d.KdbHandle.ReadMessage()
}

// support maximum queue of 100 000 per handle
func (d *KdbDatasource) getKdbSyncQueryId() uint32 {
	if d.kdbSyncQueryCounter > 100000 {
		d.kdbSyncQueryCounter = 0
	}
	d.kdbSyncQueryCounter += 1
	return d.kdbSyncQueryCounter
}

func (d *KdbDatasource) runKdbQuerySync(query *kdb.K, timeout time.Duration) (*kdb.K, error) {
	queryObj := &kdbSyncQuery{query: query, id: d.getKdbSyncQueryId(), timeout: timeout}
	d.syncQueue <- queryObj
	for {
		res := <-d.syncResChan
		if res.id != queryObj.id {
			continue
		}
		return res.result, res.err
	}
}

func (d *KdbDatasource) syncQueryRunner() {
	log.DefaultLogger.Info("Beginning synchronous query listener")
	for {
		select {
		case signal := <-d.signals:
			if signal == 3 {
				log.DefaultLogger.Info("Returning from query runner")
				return
			}
		case query := <-d.syncQueue:
			var err error
			// If handle isn't open, attempt to open
			if !d.IsOpen {
				log.DefaultLogger.Info("Handle not open, opening new handle...")
				err = d.OpenConnection()
				// Return error if unable to open handle
				if err != nil {
					log.DefaultLogger.Info("Unable to open handle on-demand in syncQueryRunner")
					d.syncResChan <- &kdbSyncRes{result: nil, err: err, id: query.id}
					continue
				}
			}
			// If handle is open, query the kdb+ process
			err = d.WriteConnection(kdb.SYNC, query.query)
			if err != nil {
				log.DefaultLogger.Error("Error writing message", err.Error())
				d.syncResChan <- &kdbSyncRes{result: nil, err: err, id: query.id}
				continue
			}

			select {
			case msg := <-d.rawReadChan:
				d.syncResChan <- &kdbSyncRes{result: msg.result, err: msg.err, id: query.id}
			case <-time.After(query.timeout):
				d.syncResChan <- &kdbSyncRes{result: nil, err: fmt.Errorf("Queried timed out after %v", query.timeout), id: query.id}
				d.CloseConnection()
			}
		}
	}
}

func (d *KdbDatasource) kdbHandleListener() {
	kdbEOF := "Failed to read message header:"
	for {
		res, _, err := d.ReadConnection()
		if err != nil {
			log.DefaultLogger.Info(err.Error())
			if strings.Contains(err.Error(), kdbEOF) {
				log.DefaultLogger.Info("Handle read error, publishing error and returning from kdbHandleListener")
				d.IsOpen = false
				d.rawReadChan <- &kdbRawRead{result: res, err: err}
				return
			}
		}
		d.rawReadChan <- &kdbRawRead{result: res, err: err}
	}
}

func buildDatasourceKdbDict(settings *backend.DataSourceInstanceSettings) *kdb.K {
	datasourceKeys := kdb.SymbolV([]string{"ID", "Name", "UID", "URL", "Updated", "User"})
	var datasourceValues *kdb.K
	if settings == nil {
		datasourceValues = kdb.NewList(
			kdb.Long(-1),
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(-kdb.KP, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)),
			kdb.Atom(kdb.KC, ""))
	} else {
		datasourceValues = kdb.NewList(
			kdb.Long(settings.ID),
			kdb.Atom(kdb.KC, settings.Name),
			kdb.Atom(kdb.KC, settings.UID),
			kdb.Atom(kdb.KC, settings.URL),
			kdb.Atom(-kdb.KP, settings.Updated),
			kdb.Atom(kdb.KC, settings.User))
	}
	return kdb.NewDict(datasourceKeys, datasourceValues)
}

func buildUserKdbDict(settings *backend.User) *kdb.K {
	userKeys := kdb.SymbolV([]string{"UserName", "UserEmail", "UserLogin", "UserRole"})
	var userValues *kdb.K
	if settings == nil {
		userValues = kdb.NewList(
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(kdb.KC, ""),
			kdb.Atom(kdb.KC, ""))
	} else {
		userValues = kdb.NewList(
			kdb.Atom(kdb.KC, settings.Name),
			kdb.Atom(kdb.KC, settings.Email),
			kdb.Atom(kdb.KC, settings.Login),
			kdb.Atom(kdb.KC, settings.Role))
	}
	return kdb.NewDict(userKeys, userValues)
}

func buildQueryKdbDict(q backend.DataQuery, qText string) *kdb.K {
	queryKeys := kdb.SymbolV([]string{"RefID", "Query", "QueryType", "MaxDataPoints", "Interval", "TimeRange"})
	queryValues := kdb.NewList(
		kdb.Atom(kdb.KC, q.RefID),
		kdb.Atom(kdb.KC, qText),
		kdb.Symbol("QUERY"),
		kdb.Long(q.MaxDataPoints),
		kdb.Long(int64(q.Interval)),
		kdb.Atom(kdb.KP, []time.Time{q.TimeRange.From, q.TimeRange.To}))
	return kdb.NewDict(queryKeys, queryValues)
}
