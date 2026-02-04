# StreamNZB

StreamNZB is a stremio addon + usenet proxy to pool all your providers into one place. 

Add into stremio with ADDON_BASE_URL/SECURITY_TOKEN/manifest.json. e.g. https://streamnzb.domain.com/IAMSECURE/manifest.json 
By default StreamNZB will try providing 2 of each resolution (4k, 1080p, 720p, sd) to stremio and they should all be available on your configured usenet provider. Availability is reported to a community endpoint so we can see which articles are available on which providers. (If you want to opt out you can build the binary yourself)

You can add StreamNZB to sabnzbd so your manual downloading will pool together with stremio streams. This is configured with the NNTP_PROXY_* variables.

### Running the Application

```
version: '3.8'

services:
  streamnzb:
    image: ghcr.io/gaisberg/streamnzb:latest
    container_name: streamnzb
    restart: unless-stopped
    ports:
      - "7000:7000"
    environment:
      - NZBHYDRA2_URL=http://nzbhydra2:5076
      - NZBHYDRA2_API_KEY=your_api_key_here
      - ADDON_PORT=7000
      - ADDON_BASE_URL=http://localhost:7000
      - CACHE_TTL_SECONDS=3600
      - VALIDATION_SAMPLE_SIZE=5
      - MAX_CONCURRENT_VALIDATIONS=20
      - PROVIDER_1_NAME=Provider1
      - PROVIDER_1_HOST=news.provider1.com
      - PROVIDER_1_PORT=563
      - PROVIDER_1_USERNAME=user
      - PROVIDER_1_PASSWORD=password
      - PROVIDER_1_CONNECTIONS=10
      - PROVIDER_1_SSL=true
      - SECURITY_TOKEN=your_secure_token
      - NNTP_PROXY_ENABLED=true
      - NNTP_PROXY_PORT=119
      - NNTP_PROXY_HOST=0.0.0.0
      - NNTP_PROXY_AUTH_USER=usenet
      - NNTP_PROXY_AUTH_PASS=usenet
```

### Adding Providers

```
PROVIDER_2_NAME=Provider2
PROVIDER_2_HOST=news.provider2.com
PROVIDER_2_PORT=563
PROVIDER_2_USERNAME=user2
PROVIDER_2_PASSWORD=password2
PROVIDER_2_CONNECTIONS=5
PROVIDER_2_SSL=true
```