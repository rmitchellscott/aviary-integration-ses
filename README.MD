# Aviary SES Integration

A containerized AWS Lambda (Go) that listens for raw email files in S3, extracts PDF/EPUB attachments, uploads those to a secondary bucket, generates short-lived presigned URLs, and posts them to a webhook. Designed for use with [Aviary](https://github.com/rmitchellscott/Aviary).

## Architecture

1. **SES** drops raw email into **S3** (raw-email bucket).
2. **S3 → Lambda** trigger fires on `ObjectCreated` events.
3. **Lambda** (Go + enmime):
   - Retrieves the email from S3.
   - Parses all attachments; filters for `.pdf` & `.epub`.
   - Uploads each attachment to **S3** (attachments bucket with prefix `attachments/`).
   - Presigns a GET URL (15 min TTL).
   - Extracts S3 prefix of file in raw email bucket to use as `rm_dir` (reMarkable folder).
   - Posts `Body=<url>&rm_dir=<original-prefix or “/”>` to provided webhook.
4. **Webhook** receives URLs + directory info for downstream processing.

## Prerequisites

- Go 1.24+, Docker, AWS CLI configured with appropriate rights.
- Two S3 buckets:
  - **Raw email** bucket (no suffix filter; any object triggers the Lambda).
  - **Attachment** bucket (where extracted files are stored).
- Aviary webhook endpoint accessible to the Lambda.
- IAM permissions (see below).

## Local Development & Testing

1. **Clone & configure**
   ```bash
   git clone git@github.com:rmitchellscott/aviary-integration-ses.git
   cd aviary-integration-ses
   go mod download
   ```

2. **Prepare sample email**
   Place a `.eml` file (with PDF/EPUB attachments) at `./sample.eml`.

3. **Build & run locally**
   ```bash
   # Build the “local” image (includes AWS Lambda RIE)
   docker build      --platform linux/amd64      --target local      -t aviary-ses-local .

   # Ensure you have AWS creds in ~/.aws or env vars
   export AWS_REGION=us-east-2
   export ATTACHMENT_BUCKET=my-attachments-bucket
   export WEBHOOK_URL=https://aviary.example.com/api/webhook
   # Optional: API key for Authorization header
   export API_KEY=your-api-key

   # Run the container
   docker run -d      --name aviary-ses-local      -p 9000:8080 \
      -e AWS_REGION -e ATTACHMENT_BUCKET -e WEBHOOK_URL -e API_KEY \
      -v ~/.aws:/root/.aws:ro aviary-ses-local

   # Invoke with a fake S3 event
   cat > event.json <<EOF
   {
     "Records": [{
       "s3": {
         "bucket": { "name": "my-raw-email-bucket" },
         "object": { "key": "Documents/test.eml" }
       }
     }]
   }
   EOF

   curl -XPOST http://localhost:9000/2015-03-31/functions/function/invocations      -d @event.json

   # Watch logs
   docker logs -f aviary-ses-local
   ```

## Docker Build (Production)

```bash
docker build   --platform linux/amd64,linux/arm64   --target base   -t 123456789123.dkr.ecr.us-east-2.amazonaws.com/aviary-integration-ses:latest .
```

---

## AWS Deployment

### 1. Push to ECR

```bash
aws ecr create-repository --repository-name aviary-integration-ses --region us-east-2

aws ecr get-login-password --region us-east-2   | docker login --username AWS       --password-stdin 123456789123.dkr.ecr.us-east-2.amazonaws.com

docker push 123456789123.dkr.ecr.us-east-2.amazonaws.com/aviary-integration-ses:latest
```

### 2. IAM Role & Policies

- **Assume role** trust policy for Lambda service.
- Attach **AWSLambdaBasicExecutionRole** for CloudWatch Logs.
- Inline policy granting:
  ```json
  {
    "Version":"2012-10-17",
    "Statement":[
      {"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::my-raw-email-bucket/*"},
      {"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::my-attachments-bucket/attachments/*"}
    ]
  }
  ```

### 3. Create Lambda (Container Image)

```bash
aws lambda create-function   --region us-east-2   --function-name aviary-integration-ses   --package-type Image   --code ImageUri=123456789123.dkr.ecr.us-east-2.amazonaws.com/aviary-integration-ses:latest   --role arn:aws:iam::123456789123:role/aviary-integration-ses-role   --environment Variables="{
    ATTACHMENT_BUCKET=my-attachments-bucket,
    WEBHOOK_URL=https://aviary.example.com/api/webhook,
    API_KEY=your-api-key
  }"
```

### 4. Wire S3 → Lambda Trigger

```bash
# Give S3 permission to invoke your Lambda
aws lambda add-permission   --region us-east-2   --function-name aviary-integration-ses   --statement-id AllowS3Invoke   --action lambda:InvokeFunction   --principal s3.amazonaws.com   --source-arn arn:aws:s3:::my-raw-email-bucket

# Create bucket notification (all object‐created events)
aws s3api put-bucket-notification-configuration   --region us-east-2   --bucket my-raw-email-bucket   --notification-configuration '{
    "LambdaFunctionConfigurations":[{
      "Id":"RawEmailTrigger",
      "LambdaFunctionArn":"arn:aws:lambda:us-east-2:123456789123:function:aviary-integration-ses",
      "Events":["s3:ObjectCreated:*"]
    }]
  }'
```

## CI/CD with GitHub Actions

- OIDC → assume IAM role (`GitHubActionsRole`)
- Build + push multi-arch images to: `123456789123.dkr.ecr.us-east-2.amazonaws.com/aviary-integration-ses`

See [`.github/workflows/build.yml`](.github/workflows/build.yml) for details.
