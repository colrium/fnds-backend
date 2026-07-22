# S3 Upload Configuration

The backend signs uploads and downloads. The frontend uploads file bytes directly
to S3 and never receives AWS credentials.

## Environment

Set these variables on the backend service:

```dotenv
AWS_ACCESS_KEY_ID=
AWS_SECRET_ACCESS_KEY=
AWS_REGION=af-south-1
AWS_S3_BUCKET=
AWS_S3_PUBLIC_BASE_URL=
AWS_S3_MAX_UPLOAD_BYTES=104857600
```

`AWS_S3_PUBLIC_BASE_URL` is optional. Leave it empty to use the bucket's standard
regional URL. The bucket can remain private because file downloads use short-lived
presigned URLs.

## Bucket CORS

Replace the example origins with the deployed frontend origins:

```json
[
  {
    "AllowedOrigins": [
      "https://app.fundis.co.ke",
      "https://staging.fundis.co.ke"
    ],
    "AllowedMethods": ["PUT"],
    "AllowedHeaders": ["*"],
    "ExposeHeaders": ["ETag"],
    "MaxAgeSeconds": 3600
  }
]
```

Keep S3 Block Public Access enabled. The backend IAM principal needs only
`s3:PutObject`, `s3:GetObject`, and `s3:DeleteObject` for
`arn:aws:s3:::<bucket>/users/*`.
