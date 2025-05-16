package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	maxUpload := 1 << 30
	sizeLimitedReader := http.MaxBytesReader(w, r.Body, int64(maxUpload))

	videoID, err := ExtractVideoID(w, r)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "could not get video id", err)
		return
	}

	userID, err := cfg.GetUserID(w, r)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "couldn't get user id", err)
		return
	}

	videoData, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "video doesn't exist", err)
		return
	}

	if videoData.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "this is not your video", nil)
		return
	}

	r.Body = sizeLimitedReader
	file, header, err := r.FormFile("video")

	defer file.Close()

	contentType := header.Header.Get("Content-Type")

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not parse media", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "invalid format", err)
		return
	}

	videoFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "cannot save video", err)
		return
	}

	defer os.Remove(videoFile.Name())
	defer videoFile.Close()

	_, err = io.Copy(videoFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not copy file", err)
		return
	}
	aspectRatio, err := getVideoAspectRatio(videoFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "cannot get aspect ratio", err)
		return
	}
	var videoLayout string
	switch aspectRatio {
	case "16:9":
		videoLayout = "landscape"
	case "9:16":
		videoLayout = "portrait"
	default:
		videoLayout = "other"
	}

	videoFile.Seek(0, io.SeekStart)

	randomBytes := make([]byte, 32)
	_, err = rand.Read(randomBytes)
	randString := hex.EncodeToString(randomBytes) + ".mp4"
	bucket := os.Getenv("S3_BUCKET")
	videoKey := fmt.Sprintf("%s/%s", videoLayout, randString)
	bucketKey := fmt.Sprintf("%s,%s", bucket, videoKey)

	processedVideo, err := processVideoForFastStart(videoFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "cannot process video", err)
		return
	}
	fastStartVideo, err := os.Open(processedVideo)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "cannot open file", err)
		return
	}
	defer fastStartVideo.Close()

	cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &bucket,
		Key:         &videoKey,
		Body:        fastStartVideo,
		ContentType: &mediaType,
	})
	videoData.VideoURL = &bucketKey
	cfg.db.UpdateVideo(videoData)

	video, err := cfg.dbVideoToSignedVideo(videoData)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "cannot assign video url", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)

	// Now you need to set up a buffer to capture stdout
	var out bytes.Buffer
	cmd.Stdout = &out

	// Run the command
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	type FFProbeResult struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}

	var result FFProbeResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		return "", err
	}

	// Make sure we have at least one stream
	if len(result.Streams) == 0 {
		return "", fmt.Errorf("no streams found in video")
	}

	// Get width and height from the first stream
	width := result.Streams[0].Width
	height := result.Streams[0].Height
	aspectRatio := float64(width) / float64(height)

	// Using a small tolerance to account for slight variations
	const tolerance = 0.1

	if math.Abs(aspectRatio-16.0/9.0) < tolerance {
		return "16:9", nil
	} else if math.Abs(aspectRatio-9.0/16.0) < tolerance {
		return "9:16", nil
	} else {
		return "other", nil
	}
}

func processVideoForFastStart(filePath string) (string, error) {
	output := filePath + ".processing"
	command := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", output)

	err := command.Run()
	if err != nil {
		return "", err
	}
	return output, nil
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(s3Client)

	presignReq, err := presignClient.PresignGetObject(
		context.Background(),
		&s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)},
		s3.WithPresignExpires(expireTime))

	if err != nil {
		return "", fmt.Errorf("cannot process request: %w", err)
	}
	return presignReq.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, fmt.Errorf("no videos")
	} else {
		splitString := strings.Split(*video.VideoURL, ",")
		bucket := splitString[0]
		key := splitString[1]
		signedURL, err := generatePresignedURL(cfg.s3Client, bucket, key, time.Duration(time.Hour))
		if err != nil {
			return video, fmt.Errorf("cannot generate presignedURL: %w", err)
		}

		video.VideoURL = &signedURL

	}

	return video, nil
}
