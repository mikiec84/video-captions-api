package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/NYTimes/video-captions-api/database"
	"github.com/NYTimes/video-captions-api/providers"
	log "github.com/Sirupsen/logrus"
)

// Client CaptionsService client
type Client struct {
	Providers map[string]providers.Provider
	DB        database.DB
	Logger    *log.Logger
	Storage   Storage
}

// GetJobs gets all jobs associated with a ParentID
func (c Client) GetJobs(parentID string) ([]*database.JobSummary, error) {
	jobs, err := c.DB.GetJobs(parentID)
	if err != nil {
		c.Logger.Errorf("Error loading jobs from DB for parent ID: %s", parentID)
		return nil, err
	}
	sort.Sort(database.ByCreatedAt(jobs))
	summaries := make([]*database.JobSummary, len(jobs))
	for i, job := range jobs {
		summaries[i] = &database.JobSummary{ID: job.ID, CreatedAt: job.CreatedAt}
	}
	return summaries, nil
}

// GetJob gets a job by ID
func (c Client) GetJob(jobID string) (*database.Job, error) {
	job, err := c.DB.GetJob(jobID)
	if err != nil {
		c.Logger.Error("Could not find Job in database")
		return nil, err
	}

	if job.Done {
		return job, nil
	}

	providerID := job.ProviderParams["ProviderID"]
	fields := log.Fields{"JobID": jobID, "Provider": job.Provider, "ProviderID": providerID}
	jobLogger := c.Logger.WithFields(fields)
	provider := c.Providers[job.Provider]
	jobLogger.Info("Fetching job from Provider")
	providerJob, err := provider.GetProviderJob(providerID)
	if err != nil {
		jobLogger.Error("error getting job from provider", err)
		return nil, err
	}

	params := providerJob.Params

	shouldUpdate := false

	for k, v := range params {
		if params[k] != job.ProviderParams[k] {
			job.ProviderParams[k] = v
			shouldUpdate = true
		}
	}

	if job.UpdateStatus(providerJob.Status, providerJob.Details) || shouldUpdate {
		err = c.DB.UpdateJob(jobID, job)
	}

	if job.Status == "delivered" && !job.Done {
		jobLogger.Info("Job is ready on the provider, downloading")
		for i, output := range job.Outputs {
			data, err := provider.Download(providerID, output.Type)
			if err != nil {
				jobLogger.WithError(err).Error("Failed to download file")
				return job, nil
			}
			jobLogger.Info("Download done, storing")
			dest, err := c.Storage.Store(data, fmt.Sprintf("%s/%s", job.Provider, output.Filename))
			if err != nil {
				jobLogger.WithError(err).Error("Failed to store file")
				return job, nil
			}
			job.Outputs[i].URL = dest
		}
		job.Done = true
		err = c.DB.UpdateJob(jobID, job)
	}
	return job, err
}

// DispatchJob dispatches a Job given an existing Provider
func (c Client) DispatchJob(job *database.Job) error {
	provider := c.Providers[job.Provider]
	jobLogger := c.Logger.WithFields(log.Fields{"JobID": job.ID, "Provider": job.Provider})
	if provider == nil {
		jobLogger.Error("Provider not found")
		return errors.New("Provider not found")
	}

	jobLogger.Info("Dispatching job to provider")
	err := provider.DispatchJob(job)
	if err != nil {
		jobLogger.Errorf("Error dispatching job to provider: %v", err)
		return fmt.Errorf("Error dispatching Job: %v", err)
	}
	jobLogger.Info("Storing job in DB")
	_, err = c.DB.StoreJob(job)
	if err != nil {
		jobLogger.Errorf("Error storing job in DB: %v", err)
		return fmt.Errorf("Error storing Job: %v", err)
	}
	return nil
}

// CancelJob cancels a job by ID
func (c Client) CancelJob(jobID string) (bool, error) {
	job, err := c.DB.GetJob(jobID)
	if err != nil {
		c.Logger.Error("Could not find Job in database")
		return false, err
	}

	if job.Done {
		c.Logger.Error("Cannot cancel a job that is already done")
		return false, nil
	}

	job.Status = "canceled"
	job.Done = true

	err = c.DB.UpdateJob(jobID, job)

	return true, err
}

// DownloadCaption downloads a caption of a given job in the specified format
func (c Client) DownloadCaption(jobID string, captionType string) ([]byte, error) {
	job, err := c.DB.GetJob(jobID)
	if err != nil {
		c.Logger.Error("Could not find Job in database")
		return nil, err
	}

	providerID := job.ProviderParams["ProviderID"]
	fields := log.Fields{"JobID": jobID, "Provider": job.Provider, "ProviderID": providerID}
	jobLogger := c.Logger.WithFields(fields)
	provider := c.Providers[job.Provider]
	jobLogger.Info("Downloading captions from provider")
	captions, err := provider.Download(providerID, captionType)
	if err != nil {
		jobLogger.Error("error downloading captions from provider", err)
		return nil, err
	}
	return captions, nil
}

// GenerateTranscript generates a transcript from the provided caption file and format
func (c Client) GenerateTranscript(captionFile []byte, captionFormat string) (string, error) {
	type SubtitleParsePreset struct {
		delimiter     string
		linesToIgnore int
		remove        string
		startingIndex int
		splitN        int
	}

	vttPreset := SubtitleParsePreset{
		delimiter:     "\n\n",
		linesToIgnore: 1,
		remove:        "",
		startingIndex: 0,
		splitN:        0,
	}

	srtPreset := SubtitleParsePreset{
		delimiter:     "\r\n\r\n",
		linesToIgnore: 2,
		remove:        "",
		startingIndex: 0,
		splitN:        0,
	}

	sbvPreset := SubtitleParsePreset{
		delimiter:     "\r\n\r\n",
		linesToIgnore: 1,
		remove:        "[br]",
		startingIndex: 0,
		splitN:        0,
	}

	ssaPreset := SubtitleParsePreset{
		delimiter:     "\n",
		linesToIgnore: 0,
		remove:        "",
		startingIndex: 4,
		splitN:        10,
	}

	var parsingPresets = make(map[string]SubtitleParsePreset)
	parsingPresets["vtt"] = vttPreset
	parsingPresets["srt"] = srtPreset
	parsingPresets["sbv"] = sbvPreset
	parsingPresets["ssa"] = ssaPreset

	if _, ok := parsingPresets[captionFormat]; ok {
		subtitleFile := string(captionFile)
		subtitleBlobs := strings.Split(subtitleFile, parsingPresets[captionFormat].delimiter)
		transcript := []string{}

		for i := parsingPresets[captionFormat].startingIndex; i < len(subtitleBlobs); i++ {
			currentBlob := subtitleBlobs[i]
			if parsingPresets[captionFormat].splitN != 0 {
				blobLines := strings.SplitN(currentBlob, ",", 10)
				transcript = append(transcript, strings.TrimSpace(blobLines[len(blobLines)-1]))
			} else {
				blobLines := strings.Split(currentBlob, "\n")
				for j := parsingPresets[captionFormat].linesToIgnore; j < len(blobLines); j++ {
					if len(blobLines[j]) > 0 {
						if parsingPresets[captionFormat].remove != "" {
							cleanString := strings.Replace(blobLines[j], parsingPresets[captionFormat].remove, " ", -1)
							transcript = append(transcript, strings.TrimSpace(cleanString))
						} else {
							transcript = append(transcript, strings.TrimSpace(blobLines[j]))
						}
					}
				}
			}
		}
		return strings.Join(transcript, " "), nil
	}
	return "", fmt.Errorf("Unable to generate a transcript for caption format: %v", captionFormat)
}
