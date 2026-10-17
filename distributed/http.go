package distributed

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

type workerRequest struct {
	WorkerID string `json:"worker_id"`
}
type leaseRequest struct {
	WorkerID string `json:"worker_id"`
	JobID    int64  `json:"job_id"`
	Epoch    uint64 `json:"epoch"`
	Output   string `json:"output"`
}
type jobRequest struct {
	ID       int64  `json:"id"`
	ToolName string `json:"tool_name"`
	Payload  string `json:"payload"`
}

func decode(w http.ResponseWriter, r *http.Request, out any) error {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var other any
	if err := d.Decode(&other); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func bad(w http.ResponseWriter, status int, e error) {
	respond(w, status, map[string]string{"error": e.Error()})
}

func (c *Coordinator) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /workers/register", func(w http.ResponseWriter, r *http.Request) {
		var req workerRequest
		if err := decode(w, r, &req); err != nil {
			bad(w, 400, err)
			return
		}
		if err := c.Register(req.WorkerID); err != nil {
			if errors.Is(err, ErrDuplicateWorker) {
				bad(w, 409, err)
			} else {
				bad(w, 400, err)
			}
			return
		}
		respond(w, 201, map[string]any{"worker_id": req.WorkerID, "registered": true})
	})
	mux.HandleFunc("POST /workers/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		var req workerRequest
		if err := decode(w, r, &req); err != nil {
			bad(w, 400, err)
			return
		}
		if err := c.Heartbeat(req.WorkerID); err != nil {
			bad(w, 404, err)
			return
		}
		respond(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /workers", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, c.Workers()) })
	mux.HandleFunc("POST /jobs", func(w http.ResponseWriter, r *http.Request) {
		var req jobRequest
		if err := decode(w, r, &req); err != nil {
			bad(w, 400, err)
			return
		}
		j, err := c.Enqueue(req.ID, req.ToolName, req.Payload)
		if err != nil {
			if errors.Is(err, ErrDuplicateJob) {
				bad(w, 409, err)
			} else {
				bad(w, 400, err)
			}
			return
		}
		respond(w, 201, j)
	})
	mux.HandleFunc("GET /jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			respond(w, 400, map[string]string{"error": "invalid id"})
			return
		}
		j, ok := c.Get(id)
		if !ok {
			bad(w, 404, ErrJobNotFound)
			return
		}
		respond(w, 200, j)
	})
	mux.HandleFunc("POST /jobs/claim", func(w http.ResponseWriter, r *http.Request) {
		var req workerRequest
		if err := decode(w, r, &req); err != nil {
			bad(w, 400, err)
			return
		}
		j, ok, err := c.Claim(req.WorkerID)
		if errors.Is(err, ErrUnknownWorker) {
			bad(w, 404, err)
			return
		}
		if err != nil {
			bad(w, 409, err)
			return
		}
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		respond(w, 200, j)
	})
	mux.HandleFunc("POST /jobs/renew", func(w http.ResponseWriter, r *http.Request) {
		var req leaseRequest
		if err := decode(w, r, &req); err != nil {
			bad(w, 400, err)
			return
		}
		j, err := c.Renew(req.JobID, req.WorkerID, req.Epoch)
		if err != nil {
			if errors.Is(err, ErrJobNotFound) {
				bad(w, 404, err)
			} else {
				bad(w, 409, err)
			}
			return
		}
		respond(w, 200, j)
	})
	mux.HandleFunc("POST /jobs/complete", func(w http.ResponseWriter, r *http.Request) {
		var req leaseRequest
		if err := decode(w, r, &req); err != nil {
			bad(w, 400, err)
			return
		}
		j, err := c.Complete(req.JobID, req.WorkerID, req.Epoch, req.Output)
		if err != nil {
			if errors.Is(err, ErrJobNotFound) {
				bad(w, 404, err)
			} else {
				bad(w, 409, err)
			}
			return
		}
		respond(w, 200, j)
	})
	return mux
}
