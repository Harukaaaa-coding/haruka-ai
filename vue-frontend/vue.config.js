module.exports = {
  // Source maps are useful locally, but they add several MiB to production output.
  productionSourceMap: false,
  css: {
    extract: {
      // Lazy routes import a shared set of independently scoped Element Plus
      // component styles. Their route order is intentionally unconstrained;
      // keeping one shared stylesheet avoids duplicating that CSS per page.
      ignoreOrder: true
    }
  },
  configureWebpack: {
    optimization: {
      // Keep the bootstrap stable while route views and their dependencies are
      // fetched only when the user enters that feature.
      runtimeChunk: 'single',
      splitChunks: {
        chunks: 'all',
        minSize: 20000,
        maxInitialRequests: 10,
        maxAsyncRequests: 16,
        cacheGroups: {
          vueCore: {
            name: 'vue-core',
            test: /[\\/]node_modules[\\/](vue|vue-router)[\\/]/,
            priority: 40,
            chunks: 'all',
            reuseExistingChunk: true
          },
          elementPlus: {
            name: 'element-plus',
            test: /[\\/]node_modules[\\/]element-plus[\\/]/,
            minChunks: 2,
            priority: 30,
            chunks: 'all',
            reuseExistingChunk: true
          },
          elementIcons: {
            name: 'element-icons',
            test: /[\\/]node_modules[\\/]@element-plus[\\/]icons-vue[\\/]/,
            priority: 25,
            chunks: 'all',
            reuseExistingChunk: true
          },
          axios: {
            name: 'axios',
            test: /[\\/]node_modules[\\/]axios[\\/]/,
            minChunks: 2,
            priority: 20,
            chunks: 'all',
            reuseExistingChunk: true
          },
          defaultVendors: false,
          default: {
            minChunks: 2,
            priority: -20,
            reuseExistingChunk: true
          }
        }
      }
    }
  },
  devServer: {
    port: 8080,
    proxy: {
      '/api': {
        target: process.env.VUE_APP_API_PROXY_TARGET || 'http://localhost:9090',
        changeOrigin: true,
        // Realtime voice shares the /api/v1 prefix and upgrades this route
        // from HTTP to WebSocket during local development.
        ws: true
      }
    }
  }
}
