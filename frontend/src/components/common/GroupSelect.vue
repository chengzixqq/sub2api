<template>
  <Select
    :model-value="modelValue"
    :options="options"
    :searchable="true"
    :option-filter="matchesPlatform"
    @open="selectedPlatform = ''"
    @update:model-value="$emit('update:modelValue', $event)"
  >
    <template #before-options>
      <GroupPlatformFilter v-model="selectedPlatform" :options="options" />
    </template>
    <template v-for="(_, name) in $slots" :key="name" #[name]="slotProps">
      <slot :name="name" v-bind="slotProps" />
    </template>
  </Select>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import Select, { type SelectOption } from './Select.vue'
import GroupPlatformFilter from './GroupPlatformFilter.vue'

defineProps<{
  modelValue: string | number | boolean | null | undefined
  options: SelectOption[]
}>()
defineEmits<{ 'update:modelValue': [value: string | number | boolean | null] }>()
const selectedPlatform = ref('')
const matchesPlatform = (option: SelectOption) => !selectedPlatform.value || option.platform === selectedPlatform.value
</script>
