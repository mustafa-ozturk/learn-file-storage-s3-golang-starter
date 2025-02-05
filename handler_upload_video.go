package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
)

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	params := s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	}
	presignedClient := s3.NewPresignClient(s3Client)
	pReq, err := presignedClient.PresignGetObject(context.Background(), &params, s3.WithPresignExpires(expireTime))
	if err != nil {
		return "", err
	}

	return pReq.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil || *video.VideoURL == "" {
		return video, nil
	}

	s := strings.Split(*video.VideoURL, ",")
	if len(s) < 2 {
		return video, nil
	}
	bucket := s[0]
	key := s[1]

	presignedURL, err := generatePresignedURL(cfg.s3Client, bucket, key, 1*time.Hour)
	if err != nil {
		return database.Video{}, err
	}

	video.VideoURL = &presignedURL

	return video, nil
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	// 1gb limit
	http.MaxBytesReader(w, r.Body, 1<<30)

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading video", videoID, "by user", userID)

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to get video metadata", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not video metadata owner", nil)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if mediaType != "video/mp4" || err != nil {
		respondWithError(w, http.StatusBadRequest, "bad mime type", err)
		return
	}

	tmpVideoFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Could not create temp video file", err)
	}
	defer os.Remove(tmpVideoFile.Name())
	defer tmpVideoFile.Close()

	io.Copy(tmpVideoFile, file)

	aspectRatio, err := getVideoAspectRatio(tmpVideoFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Could not get aspect ratio", err)
		return
	}

	aspectRatioMap := map[string]string{
		"16:9":  "landscape",
		"9:16":  "portrait",
		"other": "other",
	}

	prefix := aspectRatioMap[aspectRatio] + "/"

	// reset tempVideoFile's file pointer to the beginning so that we can read it again
	tmpVideoFile.Seek(0, io.SeekStart)

	randomBytes := make([]byte, 32)
	_, err = rand.Read(randomBytes)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "couldn't create random bytes", nil)
		return
	}
	randomBase64Str := base64.RawURLEncoding.EncodeToString(randomBytes)
	fileExtension := strings.Split(mediaType, "/")[1]
	fileName := prefix + randomBase64Str + "." + fileExtension

	processedVideoFilePath, err := processVideoForFastStart(tmpVideoFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not process video for fast start", err)
		return
	}

	processedVideo, err := os.Open(processedVideoFilePath)
	defer processedVideo.Close()
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not open processed video", err)
		return
	}

	params := s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &fileName,
		Body:        processedVideo,
		ContentType: &mediaType,
	}

	_, err = cfg.s3Client.PutObject(context.Background(), &params)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't put object on s3", err)
		return
	}

	videoURL := fmt.Sprintf("%s,%s", cfg.s3Bucket, fileName)
	video.VideoURL = &videoURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update video", err)
		return
	}
	signedVideo, err := cfg.dbVideoToSignedVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "unable to presign video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, signedVideo)
}
