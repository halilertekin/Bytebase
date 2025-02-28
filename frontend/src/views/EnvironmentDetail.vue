<template>
  <div class="py-2">
    <ArchiveBanner v-if="state.environment.rowStatus == 'ARCHIVED'" />
  </div>
  <EnvironmentForm
    v-if="state.approvalPolicy && state.backupPolicy"
    :environment="state.environment"
    :approval-policy="state.approvalPolicy"
    :backup-policy="state.backupPolicy"
    @update="doUpdate"
    @archive="doArchive"
    @restore="doRestore"
    @update-policy="updatePolicy"
  />
  <FeatureModal
    v-if="state.missingRequiredFeature != undefined"
    :feature="state.missingRequiredFeature"
    @cancel="state.missingRequiredFeature = undefined"
  />
</template>

<script lang="ts">
import { defineComponent, reactive, watchEffect } from "vue";
import ArchiveBanner from "../components/ArchiveBanner.vue";
import EnvironmentForm from "../components/EnvironmentForm.vue";
import {
  Environment,
  EnvironmentId,
  EnvironmentPatch,
  Policy,
  PolicyType,
  DefaultApprovalPolicy,
  DefaultSchedulePolicy,
  PipelineApprovalPolicyPayload,
  BackupPlanPolicyPayload,
} from "../types";
import { idFromSlug } from "../utils";
import { hasFeature, useEnvironmentStore, usePolicyStore } from "@/store";

interface LocalState {
  environment: Environment;
  showArchiveModal: boolean;
  approvalPolicy?: Policy;
  backupPolicy?: Policy;
  missingRequiredFeature?:
    | "bb.feature.approval-policy"
    | "bb.feature.backup-policy";
}

export default defineComponent({
  name: "EnvironmentDetail",
  components: {
    ArchiveBanner,
    EnvironmentForm,
  },
  props: {
    environmentSlug: {
      required: true,
      type: String,
    },
  },
  emits: ["archive"],
  setup(props, { emit }) {
    const environmentStore = useEnvironmentStore();
    const policyStore = usePolicyStore();

    const state = reactive<LocalState>({
      environment: environmentStore.getEnvironmentById(
        idFromSlug(props.environmentSlug)
      ),
      showArchiveModal: false,
    });

    const preparePolicy = () => {
      policyStore
        .fetchPolicyByEnvironmentAndType({
          environmentId: (state.environment as Environment).id,
          type: "bb.policy.pipeline-approval",
        })
        .then((policy: Policy) => {
          state.approvalPolicy = policy;
        });

      policyStore
        .fetchPolicyByEnvironmentAndType({
          environmentId: (state.environment as Environment).id,
          type: "bb.policy.backup-plan",
        })
        .then((policy: Policy) => {
          state.backupPolicy = policy;
        });
    };

    watchEffect(preparePolicy);

    const assignEnvironment = (environment: Environment) => {
      state.environment = environment;
    };

    const doUpdate = (environmentPatch: EnvironmentPatch) => {
      environmentStore
        .patchEnvironment({
          environmentId: idFromSlug(props.environmentSlug),
          environmentPatch,
        })
        .then((environment) => {
          assignEnvironment(environment);
        });
    };

    const doArchive = (environment: Environment) => {
      environmentStore
        .patchEnvironment({
          environmentId: environment.id,
          environmentPatch: {
            rowStatus: "ARCHIVED",
          },
        })
        .then((environment) => {
          emit("archive", environment);
          assignEnvironment(environment);
        });
    };

    const doRestore = (environment: Environment) => {
      environmentStore
        .patchEnvironment({
          environmentId: environment.id,
          environmentPatch: {
            rowStatus: "NORMAL",
          },
        })
        .then((environment) => {
          assignEnvironment(environment);
        });
    };

    const updatePolicy = (
      environmentId: EnvironmentId,
      type: PolicyType,
      policy: Policy
    ) => {
      if (
        type === "bb.policy.pipeline-approval" &&
        (policy.payload as PipelineApprovalPolicyPayload).value !==
          DefaultApprovalPolicy &&
        !hasFeature("bb.feature.approval-policy")
      ) {
        state.missingRequiredFeature = "bb.feature.approval-policy";
        return;
      }
      if (
        type === "bb.policy.backup-plan" &&
        (policy.payload as BackupPlanPolicyPayload).schedule !==
          DefaultSchedulePolicy &&
        !hasFeature("bb.feature.backup-policy")
      ) {
        state.missingRequiredFeature = "bb.feature.backup-policy";
        return;
      }
      policyStore
        .upsertPolicyByEnvironmentAndType({
          environmentId,
          type: type,
          policyUpsert: {
            payload: policy.payload,
          },
        })
        .then((policy: Policy) => {
          if (type === "bb.policy.pipeline-approval") {
            state.approvalPolicy = policy;
          } else if (type === "bb.policy.backup-plan") {
            state.backupPolicy = policy;
          }
        });
    };

    return {
      state,
      doUpdate,
      doArchive,
      doRestore,
      updatePolicy,
    };
  },
});
</script>
