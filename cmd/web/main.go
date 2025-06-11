// cmd/web/main.go
package main

import (
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"SummerCamp25/pkg/log"
	"SummerCamp25/pkg/optimizer"
	"SummerCamp25/pkg/scraper"
	"go.uber.org/zap"
)

const (
	stageVerifyingLiteral  = "verifying"
	stageScrapingLiteral   = "scraping"
	stageOptimizingLiteral = "optimizing"
	stageDoneLiteral       = "done"
)

type processingJob struct {
	mutex        sync.RWMutex
	currentStage string
	subscribers  map[chan string]struct{}
	wantCSVBytes []byte
	resultJSON   []byte
	processError error
}

var jobStore = struct {
	sync.RWMutex
	jobs map[string]*processingJob
}{jobs: map[string]*processingJob{}}

func newProcessingJob() *processingJob {
	return &processingJob{
		currentStage: stageVerifyingLiteral,
		subscribers:  map[chan string]struct{}{},
	}
}

func (j *processingJob) setStage(newStage string) {
	j.mutex.Lock()
	j.currentStage = newStage
	for ch := range j.subscribers {
		select {
		case ch <- newStage:
		default:
		}
	}
	j.mutex.Unlock()
}

func (j *processingJob) subscribe() chan string {
	stageChannel := make(chan string, 4)
	j.mutex.Lock()
	stageChannel <- j.currentStage
	j.subscribers[stageChannel] = struct{}{}
	j.mutex.Unlock()
	return stageChannel
}

func (j *processingJob) unsubscribe(stageChannel chan string) {
	j.mutex.Lock()
	delete(j.subscribers, stageChannel)
	close(stageChannel)
	j.mutex.Unlock()
}

func main() {
	if initError := log.Init(true); initError != nil {
		panic(initError)
	}

	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/upload", uploadHandler)
	httpMux.HandleFunc("/job/", jobHandler)
	httpMux.Handle("/", http.FileServer(http.Dir("cmd/web/static")))

	log.L().Info("server_start", zap.String("addr", ":8080"))
	if err := http.ListenAndServe(":8080", httpMux); err != nil {
		log.L().Fatal("server_exit", zap.Error(err))
	}
}

func uploadHandler(writer http.ResponseWriter, request *http.Request) {
	if parseError := request.ParseMultipartForm(10 << 20); parseError != nil {
		http.Error(writer, "invalid multipart form", http.StatusBadRequest)
		return
	}
	fileHandle, _, fileError := request.FormFile("file")
	if fileError != nil {
		http.Error(writer, "file field missing", http.StatusBadRequest)
		return
	}
	defer fileHandle.Close()

	fileBytes, readError := io.ReadAll(fileHandle)
	if readError != nil {
		http.Error(writer, "cannot read file", http.StatusBadRequest)
		return
	}

	jobIdentifier := strconv.FormatInt(time.Now().UnixNano()+int64(rand.Intn(999)), 36)
	jobInstance := newProcessingJob()
	jobInstance.wantCSVBytes = fileBytes

	jobStore.Lock()
	jobStore.jobs[jobIdentifier] = jobInstance
	jobStore.Unlock()

	go processJob(jobInstance)

	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]string{"jobID": jobIdentifier})
}

func processJob(jobInstance *processingJob) {
	jobInstance.setStage(stageVerifyingLiteral)
	campNames, extractError := optimizer.ExtractCampNames(jobInstance.wantCSVBytes)
	if extractError != nil || len(campNames) == 0 {
		jobInstance.processError = extractError
		jobInstance.setStage(stageDoneLiteral)
		return
	}

	jobInstance.setStage(stageScrapingLiteral)
	var aggregatedSessions []scraper.Session
	rootContext := context.Background()
	for _, camp := range campNames {
		scraped, scrapeError := scraper.Scrape(rootContext, []string{camp})
		if scrapeError == nil {
			aggregatedSessions = append(aggregatedSessions, scraped...)
		}
	}

	jobInstance.setStage(stageOptimizingLiteral)
	optimizedJSON, optimizeError := optimizer.Optimize(jobInstance.wantCSVBytes, aggregatedSessions)
	if optimizeError != nil {
		jobInstance.processError = optimizeError
		jobInstance.setStage(stageDoneLiteral)
		return
	}
	jobInstance.resultJSON = optimizedJSON
	jobInstance.setStage(stageDoneLiteral)
}

func jobHandler(writer http.ResponseWriter, request *http.Request) {
	trimmedPath := strings.TrimPrefix(request.URL.Path, "/job/")
	switch {
	case strings.HasSuffix(trimmedPath, "/events"):
		jobID := strings.TrimSuffix(trimmedPath, "/events")
		serveEvents(jobID, writer, request)
	case strings.HasSuffix(trimmedPath, "/schedule"):
		jobID := strings.TrimSuffix(trimmedPath, "/schedule")
		serveSchedule(jobID, writer)
	default:
		http.NotFound(writer, request)
	}
}

func serveEvents(jobID string, writer http.ResponseWriter, request *http.Request) {
	jobStore.RLock()
	jobInstance, exists := jobStore.jobs[jobID]
	jobStore.RUnlock()
	if !exists {
		http.NotFound(writer, request)
		return
	}

	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")

	stageChannel := jobInstance.subscribe()
	defer jobInstance.unsubscribe(stageChannel)

	jsonEncoder := json.NewEncoder(writer)
	for stage := range stageChannel {
		_, _ = writer.Write([]byte("data: "))
		_ = jsonEncoder.Encode(map[string]string{"stage": stage})
		_, _ = writer.Write([]byte("\n"))
		flusher.Flush()
		if stage == stageDoneLiteral {
			break
		}
	}
}

func serveSchedule(jobID string, writer http.ResponseWriter) {
	jobStore.RLock()
	jobInstance, exists := jobStore.jobs[jobID]
	jobStore.RUnlock()
	if !exists || jobInstance.resultJSON == nil {
		http.NotFound(writer, nil)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(jobInstance.resultJSON)
}
