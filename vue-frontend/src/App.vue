<template>
  <div id="app">
    <router-view v-slot="{ Component }">
      <transition name="page" mode="out-in">
        <component :is="Component" />
      </transition>
    </router-view>
  </div>
</template>

<script>
export default {
  name: 'App'
}
</script>

<!--
  Global styling lives in src/styles/ and is imported from main.js so it lands in
  the entry stylesheet ahead of every route chunk. See styles/element.css for why
  the load order matters.
-->
