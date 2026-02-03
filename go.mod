module streamnzb

go 1.25.6

require (
	github.com/MunifTanjim/go-ptt v0.14.0
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/bodgit/plumbing v1.3.0 // indirect
	github.com/bodgit/windows v1.0.1 // indirect
	github.com/chrisfarms/yenc v0.0.0-20140520125709-00bca2f8b3cb
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/javi11/rardecode/v2 v2.1.2-0.20251031153435-d6d75db6d6ca
	github.com/joho/godotenv v1.5.1
	github.com/klauspost/compress v1.18.3 // indirect
	github.com/pierrec/lz4/v4 v4.1.25 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/ulikunitz/xz v0.5.15 // indirect
	go4.org v0.0.0-20260112195520-a5071408f32f // indirect
	golang.org/x/text v0.33.0 // indirect
)

require golang.org/x/net v0.49.0

require github.com/javi11/sevenzip v0.0.0

replace github.com/javi11/sevenzip => ./pkg/external/sevenzip
