import type { CodegenConfig } from '@graphql-codegen/cli'

// Types are generated from the same schema file gqlgen serves.
const config: CodegenConfig = {
  schema: '../../x/admin/graph/schema.graphqls',
  documents: ['src/**/*.{ts,vue}', '!src/gql/**'],
  ignoreNoDocuments: true,
  generates: {
    'src/gql/': {
      preset: 'client',
      presetConfig: { fragmentMasking: false },
      config: {
        useTypeImports: true,
        enumsAsTypes: true,
        scalars: { JSON: 'any', Time: 'string', ID: 'string' },
      },
    },
  },
}
export default config
