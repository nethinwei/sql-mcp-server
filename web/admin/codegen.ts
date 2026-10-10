import type { CodegenConfig } from '@graphql-codegen/cli'

// Types are generated from the same schema files gqlgen serves.
const config: CodegenConfig = {
  schema: ['../../x/admin/graph/schema.graphqls', '../../x/admin/graph/planning.graphqls'],
  documents: ['src/**/*.{ts,vue}', '!src/gql/**'],
  ignoreNoDocuments: true,
  generates: {
    // Every schema type, the input types included: they are the shapes of a
    // workspace's items (EntityInput, RoleInput, UserInput, as
    // x/admin/configapp/items.go has them), which no operation names.
    'src/gql/schema.ts': {
      plugins: ['typescript'],
      config: {
        useTypeImports: true,
        enumsAsTypes: true,
        scalars: { JSON: 'any', Time: 'string', ID: 'string' },
      },
    },
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
