# Test Processors for Git-Sync E2E Tests

This directory contains test processor files used to validate the git-sync functionality
in the NiFi operator.

## Files

- `test_processor.py` - A simple Python file used to verify that git-sync correctly
  synchronizes content from the Git repository into the NiFi pod.

## How It Works

The chainsaw e2e test:
1. Deploys a NiFi cluster with `customComponentsGitSync` pointing to this repository
2. Git-sync sidecars/init containers sync the content to `/kubedoop/app/git-0/current/processors/`
3. The test verifies the file exists and is accessible from the NiFi container

This approach follows the Stackable pattern of using public GitHub repositories
instead of deploying an in-cluster Git server.
