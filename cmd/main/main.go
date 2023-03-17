package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"

	speech "cloud.google.com/go/speech/apiv1"
	"cloud.google.com/go/speech/apiv1/speechpb"
	"cloud.google.com/go/storage"
	"github.com/kkdai/youtube/v2"
	"github.com/sashabaranov/go-openai"
	"google.golang.org/api/option"
)

func main() {
	VideoURL := "https://www.youtube.com/watch?v=GnTKxPQfHRM"

	client := youtube.Client{}

	video, err := client.GetVideo(VideoURL)
	fmt.Println("Getting video: ", video.Title)
	if err != nil {
		panic(err)
	}

	formats := video.Formats.AudioChannels(2)
	formats.FindByQuality("tiny")

	if len(formats) == 0 {
		panic("No video formats found")
	}

	for _, format := range formats {
		fmt.Printf("%+v", format)
	}

	fmt.Println("Downloading video: ", video.Title)
	stream, _, err := client.GetStream(video, &formats[0])
	if err != nil {
		panic(err)
	}

	fmt.Println("Saving video: ", video.Title)
	file, err := os.Create(video.Title + ".mp4")
	if err != nil {
		panic(err)
	}
	defer file.Close()

	_, err = io.Copy(file, stream)
	if err != nil {
		panic(err)
	}

	// Extract the audio from the video using ffmpeg.
	audioPath := "audio.wav"
	fmt.Println("Extracting audio: ", audioPath)
	cmd := exec.Command("ffmpeg", "-i", file.Name(), "-vn", "-acodec", "pcm_s16le", "-ar", "16000", "-ac", "1", audioPath)
	err = cmd.Run()
	if err != nil {
		log.Fatalf("Error extracting audio: %v", err)
	}
	defer os.Remove(audioPath)

	ctx := context.Background()
	homeDirectory, _ := os.UserHomeDir()
	credFile := homeDirectory + "/.google/credentials.json"

	// Upload the audio file to Google Cloud Storage.
	storeClient, err := storage.NewClient(ctx, option.WithCredentialsFile(credFile))
	if err != nil {
		log.Fatalf("Error creating client: %v", err)
	}
	defer storeClient.Close()

	bucketName := "yt_summary"
	objectName := "audio.wav"

	bucket := storeClient.Bucket(bucketName)
	wc := bucket.Object(objectName).NewWriter(ctx)
	defer wc.Close()

	data, err := os.ReadFile(audioPath)
	if err != nil {
		log.Fatalf("Error reading audio file: %v", err)
	}

	fmt.Println("Uploading audio file to bucket: ", bucketName)
	if _, err := wc.Write(data); err != nil {
		log.Fatalf("Error writing audio file to bucket: %v", err)
	}

	if err := wc.Close(); err != nil {
		log.Fatalf("Error closing bucket writer: %v", err)
	}

	// Transcribe the audio using the Google Cloud Speech-to-Text API.
	speechClient, err := speech.NewClient(ctx, option.WithCredentialsFile(credFile))
	if err != nil {
		log.Fatalf("Error creating client: %v", err)
	}
	defer speechClient.Close()

	fmt.Println("Transcribing audio file: ", objectName)
	resp, err := speechClient.LongRunningRecognize(ctx, &speechpb.LongRunningRecognizeRequest{
		Config: &speechpb.RecognitionConfig{
			Encoding:        speechpb.RecognitionConfig_LINEAR16,
			LanguageCode:    "en-US",
			SampleRateHertz: 16000,
		},
		Audio: &speechpb.RecognitionAudio{
			AudioSource: &speechpb.RecognitionAudio_Uri{Uri: fmt.Sprintf("gs://%s/%s", bucketName, objectName)},
		},
	})
	if err != nil {
		log.Fatalf("Error transcribing audio: %v", err)
	}

	wait, err := resp.Wait(ctx)
	if err != nil {
		return
	}

	fmt.Println("Transcription results:")
	results := wait.GetResults()
	transcript := ""
	for _, result := range results {
		alternatives := result.GetAlternatives()
		for _, alternative := range alternatives {
			fmt.Println(alternative.GetTranscript())
			transcript += alternative.GetTranscript()
		}
	}

	// OpenAI GPT-3
	apiKey := os.Getenv("OPENAI_API_KEY")

	prompt := fmt.Sprintf("Using this youtube video transcript: (video title: %s), generate an engaging summary of the video.  Be sure to remove anything that resembles an ad that may be contained within the transcript.\n\nTranscript:\n%s", video.Title, transcript)

	fmt.Println("Generating summary: ", transcript)
	openAIClient := openai.NewClient(apiKey)
	aiResp, err := openAIClient.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model: openai.GPT3Dot5Turbo0301,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleAssistant,
					Content: prompt,
				},
			},
		},
	)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println("Summary:")
	fmt.Println(aiResp.Choices[0].Message.Content)
}
