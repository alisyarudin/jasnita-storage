<script setup>
defineProps({ title: String })
const emit = defineEmits(['close'])
</script>
<template>
  <div class="modal-bg" @mousedown.self="emit('close')">
    <div class="modal" role="dialog" :aria-label="title">
      <h2>{{ title }}</h2>
      <slot />
      <div class="foot"><slot name="foot" /></div>
    </div>
  </div>
</template>
