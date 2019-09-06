package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	nomadAPI "github.com/hashicorp/nomad/api"

	"go.uber.org/zap"

	"github.com/vx-labs/mqtt-broker/vaultacme"
)

const CNEnvKey = "TLS_CN"

type BuildSucceededNotification struct {
	Repository      string   `json:"repository"`
	Namespace       string   `json:"namespace"`
	Name            string   `json:"name"`
	DockerURL       string   `json:"docker_url"`
	Homepage        string   `json:"homepage"`
	Visibility      string   `json:"visibility"`
	BuildID         string   `json:"build_id"`
	DockerTags      []string `json:"docker_tags"`
	TriggerKind     string   `json:"trigger_kind"`
	TriggerID       string   `json:"trigger_id"`
	TriggerMetadata struct {
		DefaultBranch string `json:"default_branch"`
		Ref           string `json:"ref"`
		Commit        string `json:"commit"`
		CommitInfo    struct {
			URL     string `json:"url"`
			Message string `json:"message"`
			Date    int64  `json:"date"`
			Author  struct {
				Username  string `json:"username"`
				URL       string `json:"url"`
				AvatarURL string `json:"avatar_url"`
			} `json:"author"`
			Committer struct {
				Username  string `json:"username"`
				URL       string `json:"url"`
				AvatarURL string `json:"avatar_url"`
			} `json:"committer"`
		} `json:"commit_info"`
	} `json:"trigger_metadata"`
}

func main() {
	ctx := context.Background()
	logger, _ := zap.NewProduction()
	var ln net.Listener
	var err error
	if os.Getenv(CNEnvKey) != "" {
		config, err := vaultacme.GetConfig(ctx, os.Getenv(CNEnvKey), logger)
		if err != nil {
			panic(err)
		}
		ln, err = tls.Listen("tcp", fmt.Sprintf(":%d", 8081), config)
		if err != nil {
			panic(err)
		}
		logger.Info("listener started", zap.String("transport", "tls"), zap.Int("port", 8081))
	} else {
		ln, err = net.Listen("tcp", fmt.Sprintf(":%d", 8080))
		if err != nil {
			panic(err)
		}
		logger.Info("listener started", zap.String("transport", "tcp"), zap.Int("port", 8080))
	}
	api, err := nomadAPI.NewClient(nomadAPI.DefaultConfig())
	if err != nil {
		logger.Error("failed to start nomad client", zap.Error(err))
		logger.Sync()
		return
	}
	ch := make(chan BuildSucceededNotification, 5)
	go func() {
		for job := range ch {
			processBuildNotification(api, logger, job)
		}
	}()
	http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		defer func() {
			logger.Info("served http request",
				zap.String("http_request_method", r.Method),
				zap.String("http_request_url", r.URL.String()),
				zap.String("remote_address", r.RemoteAddr),
				zap.Duration("request_duration", time.Since(now)))
		}()
		if r.Method == http.MethodGet {
			w.WriteHeader(200)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		notification := BuildSucceededNotification{}
		err := json.NewDecoder(r.Body).Decode(&notification)
		if err != nil {
			logger.Error("failed to decode notification", zap.Error(err))
		}
		ch <- notification
	}))
	logger.Sync()
}

func processBuildNotification(api *nomadAPI.Client, logger *zap.Logger, notification BuildSucceededNotification) {
	jobs, _, err := api.Jobs().List(&nomadAPI.QueryOptions{AllowStale: false})
	if err != nil {
		logger.Error("failed to list nomad jobs", zap.Error(err))
		return
	}
	for _, jobStub := range jobs {
		if jobStub.Type != nomadAPI.JobTypeService {
			continue
		}
		job, _, err := api.Jobs().Info(jobStub.ID, nil)
		if err != nil {
			logger.Error("failed to read nomad job", zap.Error(err), zap.String("job_id", jobStub.ID))
			continue
		}
		dirty := false
		for _, taskGroup := range job.TaskGroups {
			for _, task := range taskGroup.Tasks {
				if image, ok := task.Config["image"]; ok {
					if strings.HasPrefix(image.(string), notification.DockerURL) {
						logger.Info("scheduling update", zap.String("service_id", jobStub.ID))
						task.Config["image"] = fmt.Sprintf("%s:%s", notification.DockerURL, notification.DockerTags[0])
						dirty = true
					}
				}
			}
		}
		if dirty {
			_, _, err := api.Jobs().Plan(job, false, nil)
			if err != nil {
				logger.Error("failed to plan image update", zap.Error(err), zap.String("job_id", jobStub.ID))
			} else {
				_, _, err := api.Jobs().Register(job, nil)
				if err != nil {
					logger.Error("failed to update image", zap.Error(err), zap.String("job_id", jobStub.ID))
				} else {
					logger.Info("image updated", zap.String("job_id", jobStub.ID))
				}
			}
		}
	}
}
