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
go build ./cmd/streamnzb/
if %errorlevel% neq 0 exit /b %errorlevel%

echo Build Complete!
