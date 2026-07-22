# S3 Upload Configuration

The backend signs uploads and downloads. The frontend uploads file bytes directly
to S3 and never receives AWS credentials.

## Environment

Set these variables on the backend service:

```dotenv
AWS_ACCESS_KEY_ID=
AWS_SECRET_ACCESS_KEY=
AWS_SESSION_TOKEN=
AWS_REGION=eu-west-1
AWS_S3_BUCKET=
AWS_S3_PUBLIC_BASE_URL=
AWS_S3_MAX_UPLOAD_BYTES=104857600
```

`AWS_S3_PUBLIC_BASE_URL` is optional. Leave it empty to use the bucket's standard
regional URL. The bucket can remain private because file downloads use short-lived
presigned URLs.

## Bucket CORS

The staging bucket accepts the Fundis apex domain and all HTTPS subdomains:

```json
{
  "CORSRules": [
    {
      "AllowedOrigins": [
        "https://fundis.co.ke",
        "https://*.fundis.co.ke"
      ],
      "AllowedMethods": ["GET", "PUT", "HEAD"],
      "AllowedHeaders": ["*"],
      "ExposeHeaders": ["ETag"],
      "MaxAgeSeconds": 3600
    }
  ]
}
```

Apply the versioned policy with:

```bash
aws s3api put-bucket-cors \
  --bucket fundis-bucket-staging \
  --region eu-west-1 \
  --cors-configuration file://s3-cors.json
```

Keep S3 Block Public Access enabled. The backend IAM principal needs only
`s3:PutObject`, `s3:PutObjectTagging`, `s3:GetObject`, and `s3:DeleteObject` for
`arn:aws:s3:::<bucket>/users/*`.

Configure an S3 lifecycle rule that expires objects tagged `status=pending`
after one day. Uploads receive that tag when signed and are changed to
`status=complete` after S3 verification, immediately before MongoDB persistence.
This removes uploads abandoned before the completion request.
