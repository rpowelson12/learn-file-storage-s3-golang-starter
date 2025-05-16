package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {

	videoID, err := ExtractVideoID(w, r)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not find video id", err)
		return
	}

	userID, err := cfg.GetUserID(w, r)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "couldn't get user id", err)
		return
	}

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	maxMemory := 10 << 20

	r.ParseMultipartForm(int64(maxMemory))

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Couldn't find thumbnail", err)
		return
	}

	defer file.Close()

	contentType := header.Header.Get("Content-Type")

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "id doesn't match", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "You can only modify your own video", nil)
		return
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if mediaType != "image/jpeg" && mediaType != "image/png" {
		respondWithError(w, http.StatusBadRequest, "invalid format", err)
		return
	}

	extensions, err := mime.ExtensionsByType(contentType)
	if err != nil || len(extensions) == 0 {
		respondWithError(w, http.StatusInternalServerError, "Cannot get extension from type", err)
		return
	}
	extension := extensions[0]

	randomBytes := make([]byte, 32)
	_, err = rand.Read(randomBytes)
	randString := base64.RawURLEncoding.EncodeToString(randomBytes)

	path := filepath.Join(cfg.assetsRoot, fmt.Sprintf("%s%s", randString, extension))
	filePath, err := os.Create(path)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Cannot creat file", err)
		return
	}
	defer filePath.Close()

	_, err = io.Copy(filePath, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Cannot copy file", err)
		return
	}

	thumbnailURL := fmt.Sprintf("http://localhost:8091/assets/%s%s", randString, extension)
	video.ThumbnailURL = &thumbnailURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to update video", err)
		return
	}
	respondWithJSON(w, http.StatusOK, video)
}

func ExtractVideoID(w http.ResponseWriter, r *http.Request) (uuid.UUID, error) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		return uuid.Nil, err
	}
	return videoID, nil
}

func (cfg *apiConfig) GetUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, error) {
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		return uuid.Nil, err

	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		return uuid.Nil, err
	}

	return userID, nil
}
