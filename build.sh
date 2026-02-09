#!/bin/bash
set -e

echo "Running tests..."
go test ./pkg/... -v

echo "Building Frontend..."
cd frontend
npm run build
cd ..

echo "Clearing static assets..."
rm -rf pkg/web/static/*
echo "Copying new assets..."
mkdir -p pkg/web/static
cp -r frontend/dist/* pkg/web/static/

echo "Building Go Binary..."
go build ./cmd/streamnzb/

echo "Build Complete!"
