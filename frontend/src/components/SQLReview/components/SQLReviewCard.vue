<template>
  <div
    class="divide-y divide-block-border border border-block-border rounded-sm"
  >
    <div class="flex py-2 px-4 justify-between">
      <div class="flex flex-row space-x-3 items-center">
        <h3 class="text-lg leading-6 font-medium text-main">
          {{ reviewPolicy.name }}
        </h3>
        <BBBadge
          v-if="reviewPolicy.rowStatus == 'ARCHIVED'"
          :text="$t('sql-review.disabled')"
          :can-remove="false"
          :style="'DISABLED'"
        />
      </div>
      <button
        type="button"
        class="btn-normal py-2 px-4"
        @click.prevent="emit('click', reviewPolicy)"
      >
        {{ $t("common.view") }}
      </button>
    </div>
    <div class="border-t border-block-border">
      <dl class="divide-y divide-block-border">
        <div class="grid grid-cols-4 gap-4 px-4 py-2 items-center">
          <dt class="text-sm font-medium text-control-light">
            {{ $t("common.environment") }}
          </dt>
          <dd class="mt-1 flex gap-x-2 text-sm text-main col-span-2">
            <router-link
              v-if="reviewPolicy.environment"
              :to="`/environment/${environmentSlug(reviewPolicy.environment)}`"
              class="col-span-2 text-sm font-medium text-main hover:underline"
            >
              {{ environmentName(reviewPolicy.environment) }}
            </router-link>
            <span v-else class="text-yellow-700">
              {{ $t("sql-review.create.basic-info.no-linked-environments") }}
            </span>
          </dd>
        </div>
        <div class="grid grid-cols-4 gap-4 px-4 py-2 items-center">
          <dt class="text-sm font-medium text-control-light">
            {{ $t("common.created-at") }}
          </dt>
          <dd class="mt-1 flex text-sm text-main col-span-2">
            {{ humanizeTs(reviewPolicy.createdTs) }}
          </dd>
        </div>
        <div class="grid grid-cols-4 gap-4 px-4 py-2 items-center">
          <dt class="text-sm font-medium text-control-light">
            {{ $t("common.updated-at") }}
          </dt>
          <dd class="mt-1 flex text-sm text-main col-span-2">
            {{ humanizeTs(reviewPolicy.updatedTs) }}
          </dd>
        </div>
        <div class="grid grid-cols-4 gap-4 px-4 py-2 items-center">
          <dt class="text-sm font-medium text-control-light">
            {{ $t("common.creator") }}
          </dt>
          <dd class="mt-1 flex text-sm text-main col-span-2">
            {{ reviewPolicy.creator.name }}
          </dd>
        </div>
      </dl>
    </div>
  </div>
</template>

<script lang="ts" setup>
import { PropType } from "vue";
import { useRouter } from "vue-router";
import { SQLReviewPolicy } from "@/types/sqlReview";
import { useEnvironmentStore } from "@/store";
import { environmentName } from "@/utils";

const props = defineProps({
  reviewPolicy: {
    required: true,
    type: Object as PropType<SQLReviewPolicy>,
  },
});
const emit = defineEmits(["click"]);

const router = useRouter();
const envStore = useEnvironmentStore();
</script>
