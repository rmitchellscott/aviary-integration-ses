package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jhillyerd/enmime/v2"
)

var (
	s3Client   *s3.Client
	presigner  *s3.PresignClient
	bucket     string
	webhookURL string
	apiKey     string
	urlTTL     = 15 * time.Minute
)

func init() {
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("unable to load SDK config: %v", err)
	}

	s3Client = s3.NewFromConfig(cfg)
	presigner = s3.NewPresignClient(s3Client)

	bucket = os.Getenv("ATTACHMENT_BUCKET")
	if bucket == "" {
		log.Fatal("ATTACHMENT_BUCKET env var required")
	}
	webhookURL = os.Getenv("WEBHOOK_URL")
	if webhookURL == "" {
		log.Fatal("WEBHOOK_URL env var required")
	}
	apiKey = os.Getenv("API_KEY")
}

func handler(ctx context.Context, event events.S3Event) error {
	for _, record := range event.Records {
		srcBucket := record.S3.Bucket.Name
		key := record.S3.Object.Key

		// Fetch raw email
		objOut, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(srcBucket),
			Key:    aws.String(key),
		})
		if err != nil {
			log.Printf("GetObject failed for %s: %v", key, err)
			continue
		}
		raw, err := io.ReadAll(objOut.Body)
		objOut.Body.Close()
		if err != nil {
			log.Printf("ReadAll failed for %s: %v", key, err)
			continue
		}

		// Parse email
		env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
		if err != nil {
			log.Printf("enmime parse failed for %s: %v", key, err)
			continue
		}

		// Derive original prefix for rm_dir
		rawPrefix := path.Dir(key)
		rmDir := rawPrefix
		// Special-case empty, ".", or "root" to mean the bucket root
		if rawPrefix == "" || rawPrefix == "." || rawPrefix == "root" {
			rmDir = "/"
		}

		// Process attachments
		for _, att := range env.Attachments {
			ext := strings.ToLower(path.Ext(att.FileName))
			if ext != ".pdf" && ext != ".epub" {
				continue
			}

			// Upload to attachments bucket
			attachKey := fmt.Sprintf("attachments/%s", att.FileName)
			_, err := s3Client.PutObject(ctx, &s3.PutObjectInput{
				Bucket: aws.String(bucket),
				Key:    aws.String(attachKey),
				Body:   bytes.NewReader(att.Content),
			})
			if err != nil {
				log.Printf("PutObject failed for %s: %v", attachKey, err)
				continue
			}

			// Generate a presigned URL
			pr, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(bucket),
				Key:    aws.String(attachKey),
			}, s3.WithPresignExpires(urlTTL))
			if err != nil {
				log.Printf("Presign failed for %s: %v", attachKey, err)
				continue
			}

			// Notify webhook
			form := url.Values{}
			form.Set("Body", pr.URL)
			form.Set("rm_dir", rmDir)

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, strings.NewReader(form.Encode()))
			if err != nil {
				log.Printf("create webhook request failed: %v", err)
				continue
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if apiKey != "" {
				req.Header.Set("Authorization", "Bearer "+apiKey)
			}

			rsp, err := http.DefaultClient.Do(req)
			if err != nil {
				log.Printf("POST webhook failed: %v", err)
			}
			if rsp != nil {
				rsp.Body.Close()
			}
		}
	}
	return nil
}

func main() {
	lambda.Start(handler)
}
