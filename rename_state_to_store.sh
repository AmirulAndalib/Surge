#!/bin/bash

# Move and rename package
mkdir -p internal/store
cp -r internal/engine/state/* internal/store/
find internal/store -type f -name "*.go" -exec sed -i 's/^package state$/package store/' {} +
rm -rf internal/engine/state

# Replace imports
find . -type f -name "*.go" -exec sed -i 's|"github.com/SurgeDM/Surge/internal/engine/state"|"github.com/SurgeDM/Surge/internal/store"|g' {} +

# Replace package calls
FUNCS=(
    "SaveState" "SaveStateWithOptions" "LoadState" "DeleteState" "DeleteTasks" "LoadMasterList" 
    "AddToMasterList" "RemoveFromMasterList" "GetDownload" "LoadPausedDownloads" "LoadCompletedDownloads"
    "CheckDownloadExists" "UpdateStatus" "UpdateURL" "PauseAllDownloads" "ResumeAllDownloads" 
    "ListAllDownloads" "RemoveCompletedDownloads" "RemoveFailedDownloads" "UpdateRateLimit" 
    "UpdateDefaultRateLimit" "ClearRateLimit" "NormalizeStaleDownloads" "ValidateIntegrity"
    "SaveStateOptions" "LoadStates" "Init"
)

for func in "${FUNCS[@]}"; do
    find . -type f -name "*.go" -exec sed -i "s/state\.$func/store\.$func/g" {} +
done

