package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi"
	"github.com/lucsky/cuid"

	"github.com/denkweit/distlock/types"
)

type session struct {
	ID    string `json:"id"`
	Key   string `json:"-"`
	Timer *time.Timer
}

type lockableValue struct {
	Value    string
	IsLocked bool
}

func startTimer(duration time.Duration, s *session, lock *sync.RWMutex, kvs map[string]*lockableValue, sessions map[string]*session) {
	if s.Timer != nil {
		s.Timer.Stop()
	}
	s.Timer = time.AfterFunc(duration, func() {
		lock.Lock()
		defer lock.Unlock()

		delete(kvs, s.Key)
		delete(sessions, s.ID)
	})
}

func main() {

	var port int
	flag.IntVar(&port, "port", 9876, "set port")
	flag.Parse()

	router := chi.NewRouter()

	sessions := map[string]*session{}
	kvLock := sync.RWMutex{}
	kvs := map[string]*lockableValue{}

	router.Get("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(types.StatusReturn{Running: true})
	})

	router.Post("/session/renew/{sessionId}/{duration}", func(w http.ResponseWriter, r *http.Request) {
		kvLock.Lock()
		defer kvLock.Unlock()

		sessionId := chi.URLParam(r, "sessionId")
		duration := chi.URLParam(r, "duration")

		interval, err := strconv.ParseInt(duration, 10, 64)

		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		if session, ok := sessions[sessionId]; ok {
			startTimer(time.Duration(interval), session, &kvLock, kvs, sessions)
		}
	})

	router.Post("/session/destroy/{sessionId}", func(w http.ResponseWriter, r *http.Request) {
		kvLock.Lock()
		defer kvLock.Unlock()

		sessionId := chi.URLParam(r, "sessionId")
		if session, ok := sessions[sessionId]; ok {
			session.Timer.Stop()
			delete(kvs, session.Key)
			delete(sessions, sessionId)
		}
	})

	router.Get("/kv/keys", func(w http.ResponseWriter, r *http.Request) {
		prefix := r.URL.Query().Get("prefix")
		kvLock.RLock()

		ret := []string{}
		for key := range kvs {
			if prefix != "" && key[:len(prefix)] == prefix {
				ret = append(ret, key)
			} else if prefix == "" {
				ret = append(ret, key)
			}
		}

		kvLock.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ret)
	})

	router.Post("/kv/acquire/{key}/{duration}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		kvLock.Lock()

		key := chi.URLParam(r, "key")
		duration := chi.URLParam(r, "duration")

		interval, err := strconv.ParseInt(duration, 10, 64)

		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		value := r.URL.Query().Get("value")

		ret := types.AcquireReturn{
			SessionID: cuid.New(),
			Success:   false,
		}

		if _, ok := kvs[key]; !ok {
			kvs[key] = &lockableValue{
				Value:    value,
				IsLocked: false,
			}
		}

		if !kvs[key].IsLocked {

			kvs[key].IsLocked = true

			sessions[ret.SessionID] = &session{
				ID:  ret.SessionID,
				Key: key,
			}

			startTimer(time.Duration(interval), sessions[ret.SessionID], &kvLock, kvs, sessions)

			ret.Success = true
		}

		kvLock.Unlock()
		json.NewEncoder(w).Encode(ret)
	})

	router.Post("/kv/release/{key}/{sessionId}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		key := chi.URLParam(r, "key")
		sessionId := chi.URLParam(r, "sessionId")

		kvLock.Lock()

		ret := types.ReleaseReturn{
			Success: false,
		}

		if v, ok := kvs[key]; ok {
			if session, sessionOk := sessions[sessionId]; sessionOk && session.Key == key {
				v.IsLocked = false
				ret.Success = true
			}
		}

		kvLock.Unlock()
		json.NewEncoder(w).Encode(ret)
		return

	})

	router.Post("/kv/set/{key}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		key := chi.URLParam(r, "key")
		sessionId := r.URL.Query().Get("sessionId")
		value := r.URL.Query().Get("value")

		kvLock.Lock()

		ret := types.SetReturn{
			Success: false,
		}

		if sessionId != "" {
			if session, sessionOk := sessions[sessionId]; sessionOk && session.Key == key {
				kvs[key].Value = value
				ret.Success = true
			}
		} else {
			if _, ok := kvs[key]; !ok {
				kvs[key] = &lockableValue{
					Value:    value,
					IsLocked: false,
				}
				ret.Success = true
			}
		}

		kvLock.Unlock()
		json.NewEncoder(w).Encode(ret)
		return
	})

	router.Get("/kv/get/{key}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		key := chi.URLParam(r, "key")

		kvLock.RLock()

		ret := types.GetReturn{
			Success: false,
		}

		if v, ok := kvs[key]; ok {
			ret.Success = true
			ret.Key = key
			ret.Value = v.Value
		}

		kvLock.RUnlock()
		json.NewEncoder(w).Encode(ret)
		return
	})

	err := http.ListenAndServe(":9876", router)
	if err != nil {
		panic(err)
	}
}