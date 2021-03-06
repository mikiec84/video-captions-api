package providers

import (
	"net/url"
	"strconv"

	"github.com/NYTimes/gizmo/config"
	"github.com/NYTimes/threeplay"
	captionsConfig "github.com/NYTimes/video-captions-api/config"
	"github.com/NYTimes/video-captions-api/database"
	log "github.com/Sirupsen/logrus"
)

const providerName string = "3play"

// ThreePlayProvider is a 3play client that implements the Provider interface
type ThreePlayProvider struct {
	*threeplay.Client
	logger *log.Logger
	config ThreePlayConfig
}

// ThreePlayConfig holds config necessary to create a ThreePlayProvider
type ThreePlayConfig struct {
	APIKey        string            `envconfig:"THREE_PLAY_API_KEY"`
	APISecret     string            `envconfig:"THREE_PLAY_API_SECRET"`
	FormatMapping map[string]string `envconfig:"THREE_PLAY_FORMAT_MAPPING"`
}

// New3PlayProvider creates a ThreePlayProvider instance
func New3PlayProvider(cfg *ThreePlayConfig, svcCfg *captionsConfig.CaptionsServiceConfig) Provider {
	return &ThreePlayProvider{
		threeplay.NewClient(cfg.APIKey, cfg.APISecret),
		svcCfg.Logger,
		*cfg,
	}
}

// Load3PlayConfigFromEnv loads 3play API Key/Secret from environment
func Load3PlayConfigFromEnv() ThreePlayConfig {
	var providerConfig ThreePlayConfig
	config.LoadEnvConfig(&providerConfig)
	return providerConfig
}

// GetName returns provider name
func (c *ThreePlayProvider) GetName() string {
	return providerName
}

// Download downloads captions file from specified type
func (c *ThreePlayProvider) Download(id, captionsType string) ([]byte, error) {
	fileID, err := strconv.Atoi(id)
	if err != nil {
		return nil, err
	}
	opts := threeplay.GetCaptionsOptions{
		FileID: uint(fileID),
		Format: threeplay.CaptionsFormat(captionsType),
	}
	if customFormat := c.config.FormatMapping[captionsType]; customFormat != "" {
		opts.OutputFormat = customFormat
		opts.Format = ""
	}
	return c.GetCaptions(opts)
}

// GetProviderJob returns a 3play file
func (c *ThreePlayProvider) GetProviderJob(id string) (*database.ProviderJob, error) {
	i, err := strconv.Atoi(id)
	if err != nil {
		return nil, err
	}

	file, err := c.GetFile(uint(i))
	if err != nil {
		return nil, err
	}

	providerJob := &database.ProviderJob{
		ID:      strconv.FormatUint(uint64(file.ID), 10),
		Status:  file.State,
		Details: file.ErrorDescription,
	}
	return providerJob, nil
}

// DispatchJob sends a video file to 3play for transcription and captions generation
func (c *ThreePlayProvider) DispatchJob(job *database.Job) error {
	jobLogger := c.logger.WithFields(log.Fields{"JobID": job.ID, "Provider": job.Provider})
	query := url.Values{}

	for k, v := range job.ProviderParams {
		query.Add(k, v)
	}
	fileID, err := c.UploadFileFromURL(job.MediaURL, query)

	if err != nil {
		jobLogger.Error("Failed to dispatch job to 3Play", err)
		return err
	}

	job.ProviderParams["ProviderID"] = strconv.FormatUint(uint64(fileID), 10)

	return nil
}
