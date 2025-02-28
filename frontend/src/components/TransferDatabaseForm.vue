<template>
  <TransferSingleDatabaseForm
    v-if="isTenantProject"
    :project="project"
    :transfer-source="state.transferSource"
    :database-list="databaseList"
    @dismiss="$emit('dismiss')"
    @submit="(database, labels) => transferDatabase([database], labels)"
  >
    <template #transfer-source-selector>
      <TransferSourceSelector
        :project="project"
        :transfer-source="state.transferSource"
        @change="state.transferSource = $event"
      />
    </template>
  </TransferSingleDatabaseForm>

  <TransferMultipleDatabaseForm
    v-else
    :project="project"
    :transfer-source="state.transferSource"
    :database-list="databaseList"
    @dismiss="$emit('dismiss')"
    @submit="(databaseList) => transferDatabase(databaseList)"
  >
    <template #transfer-source-selector>
      <TransferSourceSelector
        :project="project"
        :transfer-source="state.transferSource"
        @change="state.transferSource = $event"
      />
    </template>
  </TransferMultipleDatabaseForm>

  <div
    v-if="state.loading"
    class="absolute inset-0 z-10 bg-white/70 flex items-center justify-center"
  >
    <BBSpin />
  </div>
</template>

<script lang="ts" setup>
import { computed, onBeforeMount, PropType, reactive, watch } from "vue";
import { cloneDeep } from "lodash-es";
import {
  TransferSingleDatabaseForm,
  TransferMultipleDatabaseForm,
  TransferSource,
  TransferSourceSelector,
} from "@/components/TransferDatabaseForm";
import {
  Database,
  ProjectId,
  DEFAULT_PROJECT_ID,
  DatabaseLabel,
} from "../types";
import { sortDatabaseList } from "../utils";
import {
  pushNotification,
  useCurrentUser,
  useDatabaseStore,
  useEnvironmentList,
  useProjectStore,
} from "@/store";

interface LocalState {
  transferSource: TransferSource;
  loading: boolean;
}

const props = defineProps({
  projectId: {
    required: true,
    type: Number as PropType<ProjectId>,
  },
});

const emit = defineEmits<{
  (e: "dismiss"): void;
}>();

const databaseStore = useDatabaseStore();
const projectStore = useProjectStore();
const currentUser = useCurrentUser();

const state = reactive<LocalState>({
  transferSource: props.projectId === DEFAULT_PROJECT_ID ? "OTHER" : "DEFAULT",
  loading: false,
});

const project = computed(() => projectStore.getProjectById(props.projectId));

// Fetch project entity when initialize and props.projectId changes.
watch(
  () => props.projectId,
  () => projectStore.fetchProjectById(props.projectId),
  { immediate: true }
);

const isTenantProject = computed(() => project.value.tenantMode === "TENANT");

const prepareDatabaseListForDefaultProject = () => {
  databaseStore.fetchDatabaseListByProjectId(DEFAULT_PROJECT_ID);
};

onBeforeMount(prepareDatabaseListForDefaultProject);

const environmentList = useEnvironmentList(["NORMAL"]);

const databaseList = computed(() => {
  var list;
  if (state.transferSource == "DEFAULT") {
    list = cloneDeep(
      databaseStore.getDatabaseListByProjectId(DEFAULT_PROJECT_ID)
    );
  } else {
    list = cloneDeep(
      databaseStore.getDatabaseListByPrincipalId(currentUser.value.id)
    ).filter((item: Database) => item.project.id != props.projectId);
  }

  return sortDatabaseList(list, environmentList.value);
});

const transferDatabase = async (
  databaseList: Database[],
  labels?: DatabaseLabel[]
) => {
  const transferOneDatabase = (
    database: Database,
    labels?: DatabaseLabel[]
  ) => {
    return databaseStore.transferProject({
      databaseId: database.id,
      projectId: props.projectId,
      labels, // Will keep all labels if not specified here
    });
  };

  try {
    state.loading = true;
    const requests = databaseList.map((db) => transferOneDatabase(db, labels));
    await Promise.all(requests);
    const displayDatabaseName =
      databaseList.length > 1
        ? `${databaseList.length} databases`
        : `'${databaseList[0].name}'`;

    pushNotification({
      module: "bytebase",
      style: "SUCCESS",
      title: `Successfully transferred ${displayDatabaseName} to project '${project.value.name}'.`,
    });
    emit("dismiss");
  } finally {
    state.loading = false;
  }
};
</script>
