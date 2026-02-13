@echo off
echo Building Frontend...
cd frontend
call npm run build
if %errorlevel% neq 0 exit /b %errorlevel%
cd ..

echo Clearing static assets...
del /q pkg\web\static\* >nul 2>&1
echo Copying new assets...
xcopy /E /I /Y frontend\dist\* pkg\web\static\

echo Building Go Binary...
set SHORT_SHA=unknown
for /f "tokens=*" %%i in ('git rev-parse --short HEAD 2^>nul') do set SHORT_SHA=%%i
go build -ldflags="-X main.Version=dev-%SHORT_SHA%" ./cmd/streamnzb/
if %errorlevel% neq 0 exit /b %errorlevel%

echo Build Complete!
