package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/bytebase/bytebase/api"
	"github.com/bytebase/bytebase/common/log"
	"github.com/bytebase/bytebase/plugin/db"
	"go.uber.org/zap"
)

// NewDatabaseCreateTaskExecutor creates a database create task executor.
func NewDatabaseCreateTaskExecutor() TaskExecutor {
	return &DatabaseCreateTaskExecutor{}
}

// DatabaseCreateTaskExecutor is the database create task executor.
type DatabaseCreateTaskExecutor struct {
	completed int32
}

// IsCompleted tells the scheduler if the task execution has completed.
func (exec *DatabaseCreateTaskExecutor) IsCompleted() bool {
	return atomic.LoadInt32(&exec.completed) == 1
}

// GetProgress returns the task progress.
func (*DatabaseCreateTaskExecutor) GetProgress() api.Progress {
	return api.Progress{}
}

// RunOnce will run the database create task executor once.
func (exec *DatabaseCreateTaskExecutor) RunOnce(ctx context.Context, server *Server, task *api.Task) (terminated bool, result *api.TaskRunResultPayload, err error) {
	defer atomic.StoreInt32(&exec.completed, 1)
	payload := &api.TaskDatabaseCreatePayload{}
	if err := json.Unmarshal([]byte(task.Payload), payload); err != nil {
		return true, nil, fmt.Errorf("invalid create database payload: %w", err)
	}

	statement := strings.TrimSpace(payload.Statement)
	if statement == "" {
		return true, nil, fmt.Errorf("empty create database statement")
	}

	instance := task.Instance
	driver, err := server.getAdminDatabaseDriver(ctx, task.Instance, "" /* databaseName */)
	if err != nil {
		return true, nil, err
	}
	defer driver.Close(ctx)

	log.Debug("Start creating database...",
		zap.String("instance", instance.Name),
		zap.String("database", payload.DatabaseName),
		zap.String("statement", statement),
	)

	// Create a baseline migration history upon creating the database.
	// TODO(d): support semantic versioning.
	mi := &db.MigrationInfo{
		ReleaseVersion: server.profile.Version,
		Version:        payload.SchemaVersion,
		Namespace:      payload.DatabaseName,
		Database:       payload.DatabaseName,
		Environment:    instance.Environment.Name,
		Source:         db.UI,
		Type:           db.Baseline,
		Description:    "Create database",
		CreateDatabase: true,
		Force:          true,
	}
	creator, err := server.store.GetPrincipalByID(ctx, task.CreatorID)
	if err != nil {
		// If somehow we unable to find the principal, we just emit the error since it's not
		// critical enough to fail the entire operation.
		log.Error("Failed to fetch creator for composing the migration info",
			zap.Int("task_id", task.ID),
			zap.Error(err),
		)
	} else {
		mi.Creator = creator.Name
	}

	issue, err := server.store.GetIssueByPipelineID(ctx, task.PipelineID)
	if err != nil {
		// If somehow we unable to find the issue, we just emit the error since it's not
		// critical enough to fail the entire operation.
		log.Error("Failed to fetch containing issue for composing the migration info",
			zap.Int("task_id", task.ID),
			zap.Error(err),
		)
	}
	if issue == nil {
		err := fmt.Errorf("failed to fetch containing issue for composing the migration info, issue not found with pipeline ID %v", task.PipelineID)
		log.Error(err.Error(),
			zap.Int("task_id", task.ID),
			zap.Error(err),
		)
	} else {
		mi.IssueID = strconv.Itoa(issue.ID)
	}

	migrationID, _, err := driver.ExecuteMigration(ctx, mi, statement)
	if err != nil {
		return true, nil, err
	}

	// If the database creation statement executed successfully,
	// then we will create a database entry immediately
	// instead of waiting for the next schema sync cycle to sync over this newly created database.
	// This is for 2 reasons:
	// 1. Assign the proper project to the newly created database. Otherwise, the periodic schema
	// sync will place the synced db into the default project.
	// 2. Allow user to see the created database right away.
	database, err := server.store.GetDatabase(ctx, &api.DatabaseFind{InstanceID: &task.InstanceID, Name: &payload.DatabaseName})
	if err != nil {
		return true, nil, err
	}
	if database == nil {
		databaseCreate := &api.DatabaseCreate{
			CreatorID:     api.SystemBotID,
			ProjectID:     payload.ProjectID,
			InstanceID:    task.InstanceID,
			EnvironmentID: instance.EnvironmentID,
			Name:          payload.DatabaseName,
			CharacterSet:  payload.CharacterSet,
			Collation:     payload.Collation,
			Labels:        &payload.Labels,
			SchemaVersion: payload.SchemaVersion,
		}
		createdDatabase, err := server.store.CreateDatabase(ctx, databaseCreate)
		if err != nil {
			return true, nil, err
		}
		database = createdDatabase
	} else {
		// The database didn't exist before the current run so there was a race condition between sync schema and migration execution.
		// We need to update the project ID from the default project to the target project.
		updatedDatabase, err := server.store.PatchDatabase(ctx, &api.DatabasePatch{ID: database.ID, UpdaterID: api.SystemBotID, ProjectID: &payload.ProjectID})
		if err != nil {
			return true, nil, err
		}
		database = updatedDatabase
	}

	// After the task related database entry created successfully,
	// we need to update task's database_id with the newly created database immediately.
	// Here is the main reason:
	// The task database_id represents its related database entry both for creating and patching,
	// so we should sync its value right here when the related database entry created.
	taskDatabaseIDPatch := &api.TaskPatch{
		ID:         task.ID,
		UpdaterID:  api.SystemBotID,
		DatabaseID: &database.ID,
	}
	_, err = server.store.PatchTask(ctx, taskDatabaseIDPatch)
	if err != nil {
		return true, nil, err
	}

	if payload.Labels != "" {
		project, err := server.store.GetProjectByID(ctx, payload.ProjectID)
		if err != nil {
			return true, nil, fmt.Errorf("failed to find project with ID %d", payload.ProjectID)
		}
		if project == nil {
			return true, nil, fmt.Errorf("project not found with ID %d", payload.ProjectID)
		}

		// Set database labels, except bb.environment is immutable and must match instance environment.
		err = server.setDatabaseLabels(ctx, payload.Labels, database, project, database.CreatorID, false)
		if err != nil {
			return true, nil, fmt.Errorf("failed to record database labels after creating database %v", database.ID)
		}
	}

	return true, &api.TaskRunResultPayload{
		Detail:      fmt.Sprintf("Created database %q", payload.DatabaseName),
		MigrationID: migrationID,
		Version:     mi.Version,
	}, nil
}
